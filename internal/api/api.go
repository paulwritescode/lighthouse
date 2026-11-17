// Package api serves the typed local HTTP API under /api/v1. It is meant for
// long-running local clients such as the Dock desktop app. Every call except
// the health check needs a bearer token with scopes per record kind.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paulwritescode/lighthouse/internal/codecall"
	"github.com/paulwritescode/lighthouse/internal/dock"
	"github.com/paulwritescode/lighthouse/internal/store"
	"github.com/paulwritescode/lighthouse/internal/updater"
)

const (
	apiVersion   = 1
	maxBody      = 1 << 20
	maxBatchBody = 5 << 20
	maxResponse  = 10 << 20
	ratePerSec   = 200.0
	rateBurst    = 400.0
)

// API handles /api/v1. get returns the live store, which can be replaced
// when the dashboard erases and reopens the database.
type API struct {
	get   func() *store.Store
	watch *watcher
	mu    sync.Mutex
	rates map[string]*bucket
}

// New returns an API bound to a store accessor.
func New(get func() *store.Store) *API {
	return &API{get: get, watch: &watcher{get: get}, rates: map[string]*bucket{}}
}

type ctxKey int

const (
	keyToken ctxKey = iota
	keyRequestID
)

// Handler returns the routes. Mount it at /api/v1/.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, h func(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token)) {
		mux.HandleFunc(pattern, a.guard(h))
	}
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": updater.CurrentVersion()})
	})
	route("GET /api/v1/capabilities", a.capabilities)
	route("POST /api/v1/query", a.query)
	route("GET /api/v1/records/{id}", a.getRecord)
	route("POST /api/v1/records", a.saveRecord)
	route("PATCH /api/v1/records/{id}", a.updateRecord)
	route("DELETE /api/v1/records/{id}", a.deleteRecord)
	route("POST /api/v1/records/{id}/restore", a.restoreRecord)
	route("POST /api/v1/batch", a.batch)
	route("POST /api/v1/batches/{id}/undo", a.undo)
	route("GET /api/v1/changes", a.changes)
	route("GET /api/v1/stream", a.stream)
	route("GET /api/v1/search", a.search)
	route("GET /api/v1/kinds", a.listKinds)
	route("GET /api/v1/kinds/{kind}", a.getKind)
	route("PUT /api/v1/kinds/{kind}", a.putKind)
	route("GET /api/v1/links", a.getLinks)
	route("POST /api/v1/links", a.postLink)
	route("DELETE /api/v1/links", a.deleteLink)
	route("POST /api/v1/code", a.code)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint", nil, nil)
	})
	return mux
}

// ---- middleware ----

var loopbackHosts = map[string]bool{"localhost": true, "lighthouse": true, "lighthouse.localhost": true, "127.0.0.1": true, "::1": true}

func hostOK(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return loopbackHosts[strings.ToLower(strings.Trim(host, "[]"))]
}

func originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	switch origin {
	case "tauri://localhost", "http://tauri.localhost", "https://tauri.localhost":
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return loopbackHosts[strings.ToLower(strings.Trim(u.Hostname(), "[]"))]
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (a *API) allow(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	b := a.rates[id]
	if b == nil {
		b = &bucket{tokens: rateBurst, at: now}
		a.rates[id] = b
	}
	b.tokens += now.Sub(b.at).Seconds() * ratePerSec
	if b.tokens > rateBurst {
		b.tokens = rateBurst
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (a *API) guard(h func(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := newRequestID()
		w.Header().Set("X-Request-Id", rid)
		w.Header().Set("Cache-Control", "no-store")
		r = r.WithContext(context.WithValue(r.Context(), keyRequestID, rid))
		if !hostOK(r) {
			writeError(w, http.StatusMisdirectedRequest, "forbidden", "the API only answers loopback host names", nil, nil)
			return
		}
		if !originOK(r) {
			writeError(w, http.StatusForbidden, "forbidden", "cross-origin requests are not allowed", nil, nil)
			return
		}
		s := a.get()
		if s == nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "the database is unavailable", nil, nil)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lighthouse"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "a bearer token is required; create one with `lighthouse token create`", nil, nil)
			return
		}
		tok, err := s.VerifyToken(r.Context(), strings.TrimPrefix(header, prefix))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="lighthouse", error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "the token is invalid, expired or revoked", nil, nil)
			return
		}
		if !a.allow(tok.ID) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; slow down", nil, nil)
			return
		}
		limit := int64(maxBody)
		if strings.HasSuffix(r.URL.Path, "/batch") || strings.HasSuffix(r.URL.Path, "/code") {
			limit = maxBatchBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		h(w, r, s, tok)
	}
}

// ---- responses ----

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Details   any    `json:"details,omitempty"`
		Current   any    `json:"current,omitempty"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(b) > maxResponse {
		writeError(w, http.StatusInsufficientStorage, "internal", "the response is larger than 10 MiB; narrow the query", nil, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string, details, current any) {
	var e errorBody
	e.Error.Code, e.Error.Message, e.Error.Details, e.Error.Current = code, message, details, current
	e.Error.RequestID = w.Header().Get("X-Request-Id")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(e)
	_, _ = w.Write(append(b, '\n'))
}

// fail maps a store or dock error onto the documented error model.
func fail(w http.ResponseWriter, err error) {
	var conflict *store.RevisionConflictError
	var verr *store.ValidationError
	var berr *store.BatchError
	var derr *dock.Error
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "the request body is too large", nil, nil)
	case errors.As(err, &berr):
		failIndexed(w, berr.Err, berr.Index)
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "revision_conflict", "the record changed; merge and retry", nil, conflict.Current)
	case errors.As(err, &verr):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", verr.Error(), map[string]any{"errors": verr.Errors}, nil)
	case errors.As(err, &derr):
		status := map[string]int{"not_found": 404, "validation_failed": 422, "source_missing": 422, "forbidden_for_agents": 403, "dock_not_installed": 409, "too_many_open_asks": 429}[derr.Code]
		if status == 0 {
			status = 400
		}
		writeError(w, status, derr.Code, derr.Message, derr.Details, nil)
	case errors.Is(err, store.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "no such record", nil, nil)
	case errors.Is(err, store.ErrIDExists):
		writeError(w, http.StatusConflict, "id_exists", err.Error(), nil, nil)
	case errors.Is(err, store.ErrDeleted):
		writeError(w, http.StatusConflict, "deleted", err.Error(), nil, nil)
	case errors.Is(err, store.ErrInvalidQuery):
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error(), nil, nil)
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error(), nil, nil)
	default:
		log.Printf("api: internal error: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong; see the Lighthouse log", nil, nil)
	}
}

// failIndexed reports a batch failure and the index of the operation behind it.
func failIndexed(w http.ResponseWriter, err error, index int) {
	rec := &captureWriter{header: http.Header{}}
	rec.header.Set("X-Request-Id", w.Header().Get("X-Request-Id"))
	fail(rec, err)
	var body errorBody
	if json.Unmarshal(rec.body.Bytes(), &body) == nil {
		details, _ := body.Error.Details.(map[string]any)
		if details == nil {
			details = map[string]any{}
		}
		details["index"] = index
		body.Error.Details = details
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		b, _ := json.Marshal(body)
		_, _ = w.Write(append(b, '\n'))
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), map[string]any{"index": index}, nil)
}

type captureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(status int)      { c.status = status }
func (c *captureWriter) Write(b []byte) (int, error) { return c.body.Write(b) }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, err)
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON: "+err.Error(), nil, nil)
		return false
	}
	return true
}

func forbid(w http.ResponseWriter, verb, kind string) {
	writeError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("this token cannot %s records of kind %q", verb, kind), nil, nil)
}

// allowsGlob checks a verb against a kind pattern such as dock_*.
func allowsGlob(t store.Token, verb, pattern string) bool {
	if t.CoversAllKinds(verb) {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return t.Allows(verb, pattern)
	}
	prefix := strings.TrimSuffix(pattern, "*")
	for _, sc := range t.Scopes {
		parts := strings.SplitN(sc, ":", 2)
		if len(parts) != 2 || (parts[0] != verb && !(verb == "read" && parts[0] == "write")) {
			continue
		}
		if strings.HasSuffix(parts[1], "*") && strings.HasPrefix(prefix, strings.TrimSuffix(parts[1], "*")) {
			return true
		}
	}
	return false
}

// readPatterns lists the kind patterns a token may read.
func readPatterns(t store.Token) []string {
	var out []string
	for _, sc := range t.Scopes {
		if parts := strings.SplitN(sc, ":", 2); len(parts) == 2 {
			out = append(out, parts[1])
		}
	}
	return out
}

// ---- handlers ----

func (a *API) capabilities(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	head, _ := s.Head(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"version": updater.CurrentVersion(), "api": apiVersion,
		"features": []string{"query", "changes", "stream", "batch", "undo", "kinds", "links", "soft_delete", "idempotency", "dock_module", "code"},
		"limits":   map[string]any{"body_bytes": maxBody, "batch_body_bytes": maxBatchBody, "batch_ops": 500, "page_size": 1000, "rate_per_second": ratePerSec},
		"head":     head, "token": map[string]any{"name": t.Name, "scopes": t.Scopes},
	})
}

func (a *API) query(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	var spec store.QuerySpec
	if !decode(w, r, &spec) {
		return
	}
	if !t.CoversAllKinds("read") {
		if spec.Kind == "" || !allowsGlob(t, "read", spec.Kind) {
			forbid(w, "read", firstNonEmpty(spec.Kind, "*"))
			return
		}
	}
	res, err := s.Query(r.Context(), spec)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *API) getRecord(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	rec, err := s.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if rec.DeletedAt != nil && r.URL.Query().Get("include_deleted") != "1" {
		writeError(w, http.StatusNotFound, "not_found", "no such record", nil, nil)
		return
	}
	if !t.Allows("read", rec.Kind) {
		forbid(w, "read", rec.Kind)
		return
	}
	w.Header().Set("ETag", strconv.FormatInt(rec.Revision, 10))
	writeJSON(w, http.StatusOK, rec)
}

type writeBody struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Content string         `json:"content"`
	Project string         `json:"project"`
	Agent   string         `json:"agent"`
	Kind    string         `json:"kind"`
	Fields  map[string]any `json:"fields"`
	Tags    []string       `json:"tags"`
	Actor   string         `json:"actor"`
	BatchID string         `json:"batch_id"`
}

func actorFor(t store.Token, requested string) string {
	if requested != "" {
		return requested
	}
	return "api:" + t.Name
}

// idempotent runs fn at most once per Idempotency-Key and replays the answer.
func (a *API) idempotent(w http.ResponseWriter, r *http.Request, s *store.Store, body []byte, fn func() (int, any, error)) {
	key := r.Header.Get("Idempotency-Key")
	var hash string
	if key != "" {
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		hash = hex.EncodeToString(sum[:])
		stored, found, err := s.IdempotentLookup(r.Context(), key, hash)
		if err != nil {
			fail(w, err)
			return
		}
		if found {
			var env struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			}
			if json.Unmarshal(stored, &env) == nil {
				w.Header().Set("Idempotent-Replayed", "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(env.Status)
				_, _ = w.Write(append(env.Body, '\n'))
				return
			}
		}
	}
	status, v, err := fn()
	if err != nil {
		fail(w, err)
		return
	}
	if key != "" {
		b, _ := json.Marshal(v)
		env, _ := json.Marshal(map[string]any{"status": status, "body": json.RawMessage(b)})
		_ = s.IdempotentStore(r.Context(), key, hash, env)
	}
	writeJSON(w, status, v)
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, err)
		return nil, false
	}
	return b, true
}

func (a *API) saveRecord(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var in writeBody
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON: "+err.Error(), nil, nil)
		return
	}
	kind := firstNonEmpty(in.Kind, "result")
	if !t.Allows("write", kind) {
		forbid(w, "write", kind)
		return
	}
	a.idempotent(w, r, s, raw, func() (int, any, error) {
		res, err := s.SaveArtifactWith(r.Context(), store.SaveInput{ID: in.ID, Title: in.Title, Content: in.Content, Project: in.Project, Agent: firstNonEmpty(in.Agent, "api"), Kind: kind, Fields: in.Fields, Tags: in.Tags}, store.WriteOptions{Actor: actorFor(t, in.Actor), BatchID: in.BatchID})
		if err != nil {
			return 0, nil, err
		}
		status := http.StatusCreated
		if res.Unchanged {
			status = http.StatusOK
		}
		return status, res, nil
	})
}

type updateBody struct {
	Fields     map[string]any `json:"fields"`
	IfRevision *int64         `json:"if_revision"`
	Merge      string         `json:"merge"`
	Unset      []string       `json:"unset"`
	Actor      string         `json:"actor"`
	BatchID    string         `json:"batch_id"`
}

func (a *API) updateRecord(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	var in updateBody
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	cur, err := s.GetArtifact(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if !t.Allows("write", cur.Kind) {
		forbid(w, "write", cur.Kind)
		return
	}
	if k, ok := in.Fields["kind"].(string); ok && k != cur.Kind && !t.Allows("write", k) {
		forbid(w, "write", k)
		return
	}
	if in.IfRevision == nil {
		if m := r.Header.Get("If-Match"); m != "" {
			if n, err := strconv.ParseInt(strings.Trim(m, `"`), 10, 64); err == nil {
				in.IfRevision = &n
			}
		}
	}
	res, err := s.UpdateArtifactWith(r.Context(), id, in.Fields, store.UpdateOptions{WriteOptions: store.WriteOptions{Actor: actorFor(t, in.Actor), BatchID: in.BatchID}, IfRevision: in.IfRevision, Merge: in.Merge, Unset: in.Unset})
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("ETag", strconv.FormatInt(res.Artifact.Revision, 10))
	writeJSON(w, http.StatusOK, res)
}

func (a *API) deleteRecord(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	id := r.PathValue("id")
	cur, err := s.GetArtifact(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if !t.Allows("write", cur.Kind) {
		forbid(w, "write", cur.Kind)
		return
	}
	soft := r.URL.Query().Get("soft") != "0"
	if !soft && cur.DeletedAt == nil {
		writeError(w, http.StatusConflict, "invalid_request", "only a soft-deleted record can be purged; delete it first", nil, nil)
		return
	}
	rec, err := s.DeleteArtifactWith(r.Context(), id, soft, store.WriteOptions{Actor: actorFor(t, r.URL.Query().Get("actor")), BatchID: r.URL.Query().Get("batch_id")})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": rec, "soft": soft})
}

func (a *API) restoreRecord(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	id := r.PathValue("id")
	cur, err := s.GetArtifact(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if !t.Allows("write", cur.Kind) {
		forbid(w, "write", cur.Kind)
		return
	}
	rec, err := s.RestoreArtifact(r.Context(), id, store.WriteOptions{Actor: actorFor(t, ""), BatchID: r.URL.Query().Get("batch_id")})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": rec})
}

type batchBody struct {
	Ops     []store.BatchOp `json:"ops"`
	BatchID string          `json:"batch_id"`
	Atomic  *bool           `json:"atomic"`
	Actor   string          `json:"actor"`
}

func (a *API) batch(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var in batchBody
	if err := json.Unmarshal(raw, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON: "+err.Error(), nil, nil)
		return
	}
	for i, op := range in.Ops {
		kind := ""
		switch op.Op {
		case "save":
			if op.Save != nil {
				kind = firstNonEmpty(op.Save.Kind, "result")
			}
		default:
			rec, err := s.GetArtifact(r.Context(), op.ID)
			if err != nil {
				failIndexed(w, err, i)
				return
			}
			kind = rec.Kind
		}
		if kind != "" && !t.Allows("write", kind) {
			forbid(w, "write", kind)
			return
		}
	}
	atomic := in.Atomic == nil || *in.Atomic
	a.idempotent(w, r, s, raw, func() (int, any, error) {
		res, err := s.Batch(r.Context(), in.Ops, store.BatchOptions{WriteOptions: store.WriteOptions{Actor: actorFor(t, in.Actor), BatchID: in.BatchID}, Atomic: atomic})
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, res, nil
	})
}

func (a *API) undo(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	if !t.CoversAllKinds("write") {
		forbid(w, "write", "*")
		return
	}
	res, err := s.BatchUndo(r.Context(), r.PathValue("id"), store.WriteOptions{Actor: actorFor(t, "")})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func splitList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// visibleKinds narrows a requested kind filter to what the token may read.
func visibleKinds(t store.Token, requested []string) ([]string, bool) {
	if t.CoversAllKinds("read") {
		return requested, true
	}
	if len(requested) == 0 {
		return readPatterns(t), true
	}
	for _, k := range requested {
		if !allowsGlob(t, "read", k) {
			return nil, false
		}
	}
	return requested, true
}

func (a *API) changes(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	kinds, ok := visibleKinds(t, splitList(q.Get("kinds")))
	if !ok {
		forbid(w, "read", q.Get("kinds"))
		return
	}
	page, err := s.Changes(r.Context(), store.ChangeQuery{Since: since, Limit: limit, Kinds: kinds, Projects: splitList(q.Get("projects")), ActorsNot: splitList(q.Get("actors_not"))})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) search(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("project"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]store.Artifact, 0, len(items))
	for _, it := range items {
		if t.Allows("read", it.Kind) {
			out = append(out, it)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) listKinds(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	kinds, err := s.ListKinds(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(kinds))
	for _, k := range kinds {
		if name, _ := k["kind"].(string); t.Allows("read", name) {
			out = append(out, k)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": out})
}

func (a *API) getKind(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	kind := r.PathValue("kind")
	if !t.Allows("read", kind) {
		forbid(w, "read", kind)
		return
	}
	spec, err := s.GetKind(r.Context(), kind)
	if err != nil {
		fail(w, err)
		return
	}
	if spec == nil {
		writeError(w, http.StatusNotFound, "not_found", "that kind is not registered", nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (a *API) putKind(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	if !t.CoversAllKinds("write") {
		forbid(w, "write", "*")
		return
	}
	var spec store.KindSpec
	if !decode(w, r, &spec) {
		return
	}
	spec.Kind = r.PathValue("kind")
	got, err := s.RegisterKind(r.Context(), spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil, nil)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (a *API) getLinks(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	q := r.URL.Query()
	id := q.Get("id")
	rec, err := s.GetArtifact(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if !t.Allows("read", rec.Kind) {
		forbid(w, "read", rec.Kind)
		return
	}
	links, err := s.Links(r.Context(), id, q.Get("rel"), q.Get("direction"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": links})
}

type linkBody struct {
	From string         `json:"from"`
	Rel  string         `json:"rel"`
	To   string         `json:"to"`
	Meta map[string]any `json:"meta"`
}

func (a *API) writableLink(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token, in linkBody) bool {
	for _, id := range []string{in.From, in.To} {
		rec, err := s.GetArtifact(r.Context(), id)
		if err != nil {
			fail(w, err)
			return false
		}
		if !t.Allows("write", rec.Kind) && !(id == in.To && t.Allows("read", rec.Kind)) {
			forbid(w, "write", rec.Kind)
			return false
		}
	}
	return true
}

func (a *API) postLink(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	var in linkBody
	if !decode(w, r, &in) || !a.writableLink(w, r, s, t, in) {
		return
	}
	l, err := s.AddLink(r.Context(), in.From, in.Rel, in.To, in.Meta)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil, nil)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (a *API) deleteLink(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	var in linkBody
	if !decode(w, r, &in) || !a.writableLink(w, r, s, t, in) {
		return
	}
	removed, err := s.RemoveLink(r.Context(), in.From, in.Rel, in.To)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

type codeBody struct {
	Script  string `json:"script"`
	Project string `json:"project"`
	Session string `json:"session"`
}

// code runs a Starlark script with the same sandbox and limits as the CLI. It
// is not scoped per kind, so it needs a token that can read and write all kinds.
func (a *API) code(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	if !t.CoversAllKinds("write") {
		forbid(w, "write", "*")
		return
	}
	var in codeBody
	if !decode(w, r, &in) {
		return
	}
	var out bytes.Buffer
	run := codecall.Runner{Store: s, Actor: "api:" + t.Name}
	if err := run.Run(r.Context(), in.Script, in.Session, in.Project, &out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), map[string]any{"output": out.String()}, nil)
		return
	}
	emitted := []json.RawMessage{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" && json.Valid([]byte(line)) {
			emitted = append(emitted, json.RawMessage(line))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"emitted": emitted})
}

// ---- stream ----

// watcher polls the change head once for all subscribers, so a write made by
// another process (an agent running the CLI) reaches stream clients quickly.
type watcher struct {
	get     func() *store.Store
	mu      sync.Mutex
	started bool
	head    int64
	wake    chan struct{}
	users   int
}

func (wt *watcher) subscribe() (func() <-chan struct{}, func()) {
	wt.mu.Lock()
	if wt.wake == nil {
		wt.wake = make(chan struct{})
	}
	wt.users++
	if !wt.started {
		wt.started = true
		go wt.loop()
	}
	wt.mu.Unlock()
	next := func() <-chan struct{} {
		wt.mu.Lock()
		defer wt.mu.Unlock()
		return wt.wake
	}
	return next, func() {
		wt.mu.Lock()
		wt.users--
		wt.mu.Unlock()
	}
}

func (wt *watcher) loop() {
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		wt.mu.Lock()
		idle := wt.users == 0
		if idle {
			wt.started = false
			wt.mu.Unlock()
			return
		}
		wt.mu.Unlock()
		s := wt.get()
		if s == nil {
			continue
		}
		head, err := s.Head(context.Background())
		if err != nil {
			continue
		}
		wt.mu.Lock()
		if head != wt.head {
			wt.head = head
			close(wt.wake)
			wt.wake = make(chan struct{})
		}
		wt.mu.Unlock()
	}
}

func (a *API) stream(w http.ResponseWriter, r *http.Request, s *store.Store, t store.Token) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "streaming is not supported here", nil, nil)
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		if n, err := strconv.ParseInt(last, 10, 64); err == nil {
			since = n
		}
	}
	kinds, allowed := visibleKinds(t, splitList(q.Get("kinds")))
	if !allowed {
		forbid(w, "read", q.Get("kinds"))
		return
	}
	projects := splitList(q.Get("projects"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
	next, release := a.watch.subscribe()
	defer release()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		wake := next()
		for {
			page, err := s.Changes(r.Context(), store.ChangeQuery{Since: since, Limit: 500, Kinds: kinds, Projects: projects})
			if err != nil {
				return
			}
			if page.Reset {
				fmt.Fprintf(w, "event: reset\ndata: {\"head\":%d}\n\n", page.Head)
				flusher.Flush()
				return
			}
			for _, c := range page.Items {
				b, _ := json.Marshal(c)
				fmt.Fprintf(w, "id: %d\nevent: change\ndata: %s\n\n", c.Seq, b)
			}
			// Advance past filtered-out changes so they are not re-read.
			if page.NextSeq > since {
				since = page.NextSeq
			}
			flusher.Flush()
			if len(page.Items) < 500 {
				break
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// ---- discovery ----

// DiscoveryPath is where `serve` records how to reach the API.
func DiscoveryPath(configDir string) string {
	return filepath.Join(configDir, "lighthouse", "api.json")
}

// WriteDiscovery records the listening address so clients can find the API.
func WriteDiscovery(path, addr string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{"version": updater.CurrentVersion(), "api": apiVersion, "url": "http://" + addr, "pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
