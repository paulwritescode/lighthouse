package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/paulwritescode/lighthouse/internal/dock"
	"github.com/paulwritescode/lighthouse/internal/store"
)

type env struct {
	srv    *httptest.Server
	store  *store.Store
	admin  string // read:* write:*
	dock   string // read:* write:dock_*
	narrow string // read:dock_* only
}

func setup(t *testing.T) *env {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := dock.Install(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	mk := func(name string, scopes ...string) string {
		secret, _, err := s.CreateToken(context.Background(), name, scopes, 0)
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	srv := httptest.NewServer(New(func() *store.Store { return s }).Handler())
	t.Cleanup(srv.Close)
	return &env{srv: srv, store: s, admin: mk("admin", "read:*", "write:*"), dock: mk("dock", "read:*", "write:dock_*"), narrow: mk("narrow", "read:dock_*")}
}

func (e *env) do(t *testing.T, method, path, token string, body any, headers ...string) (int, map[string]any, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestAuthenticationAndHostChecks(t *testing.T) {
	e := setup(t)
	if code, _, _ := e.do(t, "GET", "/api/v1/health", "", nil); code != 200 {
		t.Fatalf("health is open, got %d", code)
	}
	if code, body, _ := e.do(t, "GET", "/api/v1/capabilities", "", nil); code != 401 || errCode(body) != "unauthorized" {
		t.Fatalf("a token is required: %d %v", code, body)
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/capabilities", "lh_bad_token", nil); code != 401 {
		t.Fatalf("a bad token is refused: %d", code)
	}
	if code, body, _ := e.do(t, "GET", "/api/v1/capabilities", e.dock, nil, "Host", "evil.example.com"); code != 421 || errCode(body) != "forbidden" {
		t.Fatalf("a non-loopback Host must be refused (DNS rebinding): %d %v", code, body)
	}
	if code, _, _ := e.do(t, "GET", "/api/v1/capabilities", e.dock, nil, "Origin", "https://evil.example.com"); code != 403 {
		t.Fatalf("a foreign Origin must be refused: %d", code)
	}
	code, body, _ := e.do(t, "GET", "/api/v1/capabilities", e.dock, nil, "Origin", "tauri://localhost")
	if code != 200 || body["api"] != float64(1) {
		t.Fatalf("capabilities: %d %v", code, body)
	}
	if code, _, _ = e.do(t, "GET", "/api/v1/nope", e.dock, nil); code != 404 {
		t.Fatalf("unknown routes are 404, got %d", code)
	}
	if _, _, h := e.do(t, "GET", "/api/v1/capabilities", e.dock, nil); h.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("the API must not send CORS headers")
	}
}

func TestScopesLimitWhatATokenCanTouch(t *testing.T) {
	e := setup(t)
	task := map[string]any{"title": "Task", "content": "c", "kind": "dock_task", "project": "p", "fields": map[string]any{"status": "todo"}}
	code, body, _ := e.do(t, "POST", "/api/v1/records", e.dock, task)
	if code != 201 {
		t.Fatalf("a dock token can write dock kinds: %d %v", code, body)
	}
	id := body["artifact"].(map[string]any)["id"].(string)
	if code, body, _ = e.do(t, "POST", "/api/v1/records", e.dock, map[string]any{"title": "Memory", "content": "c", "kind": "result"}); code != 403 || errCode(body) != "forbidden" {
		t.Fatalf("a dock token cannot write other kinds: %d %v", code, body)
	}
	if code, body, _ = e.do(t, "POST", "/api/v1/records", e.narrow, task); code != 403 {
		t.Fatalf("a read-only token cannot write: %d %v", code, body)
	}
	if code, _, _ = e.do(t, "GET", "/api/v1/records/"+id, e.narrow, nil); code != 200 {
		t.Fatalf("a read:dock_* token can read dock records: %d", code)
	}
	memory := mustSave(t, e.store, "Private memory", "result")
	if code, _, _ = e.do(t, "GET", "/api/v1/records/"+memory, e.narrow, nil); code != 403 {
		t.Fatalf("a narrow token cannot read memory: %d", code)
	}
	if code, _, _ = e.do(t, "PATCH", "/api/v1/records/"+memory, e.dock, map[string]any{"fields": map[string]any{"x": 1}}); code != 403 {
		t.Fatalf("a dock token cannot edit memory: %d", code)
	}
	if code, _, _ = e.do(t, "POST", "/api/v1/query", e.narrow, map[string]any{}); code != 403 {
		t.Fatalf("an unscoped query needs read:*: %d", code)
	}
	if code, body, _ = e.do(t, "POST", "/api/v1/query", e.narrow, map[string]any{"kind": "dock_*"}); code != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("a scoped glob query works: %d %v", code, body)
	}
	if code, _, _ = e.do(t, "POST", "/api/v1/query", e.narrow, map[string]any{"kind": "result"}); code != 403 {
		t.Fatalf("querying outside the scope fails: %d", code)
	}
	if code, _, _ = e.do(t, "POST", "/api/v1/code", e.dock, map[string]any{"script": "emit({})"}); code != 403 {
		t.Fatalf("the code endpoint needs full access: %d", code)
	}
	if code, _, _ = e.do(t, "PUT", "/api/v1/kinds/anything", e.dock, map[string]any{}); code != 403 {
		t.Fatalf("registering kinds needs full access: %d", code)
	}
	_, list, _ := e.do(t, "GET", "/api/v1/search?q=Private", e.narrow, nil)
	if len(list["items"].([]any)) != 0 {
		t.Fatal("search must not leak records outside the scope")
	}
}

func mustSave(t *testing.T, s *store.Store, title, kind string) string {
	t.Helper()
	res, err := s.SaveArtifactWith(context.Background(), store.SaveInput{Title: title, Content: "c", Kind: kind}, store.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Artifact.ID
}

func TestIdempotencyAndRevisionConflicts(t *testing.T) {
	e := setup(t)
	task := map[string]any{"title": "Once", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "todo"}}
	_, first, _ := e.do(t, "POST", "/api/v1/records", e.dock, task, "Idempotency-Key", "k-1")
	_, second, h := e.do(t, "POST", "/api/v1/records", e.dock, task, "Idempotency-Key", "k-1")
	if h.Get("Idempotent-Replayed") != "true" || first["artifact"].(map[string]any)["id"] != second["artifact"].(map[string]any)["id"] {
		t.Fatalf("a repeated key must replay the first answer: %v %v", first, second)
	}
	if n, _ := e.store.Query(context.Background(), store.QuerySpec{Kind: "dock_task", Count: true}); *n.Count != 1 {
		t.Fatalf("the retry must not create a second record, found %d", *n.Count)
	}
	other := map[string]any{"title": "Different", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "todo"}}
	if code, body, _ := e.do(t, "POST", "/api/v1/records", e.dock, other, "Idempotency-Key", "k-1"); code != 422 {
		t.Fatalf("the same key with a different body is refused: %d %v", code, body)
	}

	id := first["artifact"].(map[string]any)["id"].(string)
	if code, body, hdr := e.do(t, "PATCH", "/api/v1/records/"+id, e.dock, map[string]any{"fields": map[string]any{"status": "doing"}}, "If-Match", `"1"`); code != 200 || hdr.Get("ETag") != "2" {
		t.Fatalf("patch with a matching If-Match: %d %v", code, body)
	}
	code, body, _ := e.do(t, "PATCH", "/api/v1/records/"+id, e.dock, map[string]any{"fields": map[string]any{"status": "done"}, "if_revision": 1})
	cur, _ := body["error"].(map[string]any)["current"].(map[string]any)
	if code != 409 || errCode(body) != "revision_conflict" || cur["revision"] != float64(2) {
		t.Fatalf("a stale revision returns the current record: %d %v", code, body)
	}
	if code, body, _ = e.do(t, "PATCH", "/api/v1/records/"+id, e.dock, map[string]any{"fields": map[string]any{"status": "bogus"}}); code != 422 || errCode(body) != "validation_failed" {
		t.Fatalf("schema violations are 422: %d %v", code, body)
	}
}

func TestBatchUndoChangesAndBodyLimits(t *testing.T) {
	e := setup(t)
	bad := map[string]any{"ops": []any{
		map[string]any{"op": "save", "save": map[string]any{"title": "A", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "todo"}}},
		map[string]any{"op": "save", "save": map[string]any{"title": "B", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "nope"}}},
	}}
	code, body, _ := e.do(t, "POST", "/api/v1/batch", e.dock, bad)
	details, _ := body["error"].(map[string]any)["details"].(map[string]any)
	if code != 422 || details["index"] != float64(1) {
		t.Fatalf("a failed batch names the operation: %d %v", code, body)
	}
	if n, _ := e.store.Query(context.Background(), store.QuerySpec{Kind: "dock_task", Count: true}); *n.Count != 0 {
		t.Fatal("a failed atomic batch must apply nothing")
	}
	good := map[string]any{"ops": []any{
		map[string]any{"op": "save", "save": map[string]any{"title": "A", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "todo"}}},
		map[string]any{"op": "save", "save": map[string]any{"title": "B", "content": "c", "kind": "dock_task", "fields": map[string]any{"status": "todo"}}},
	}}
	code, body, _ = e.do(t, "POST", "/api/v1/batch", e.dock, good)
	if code != 200 {
		t.Fatalf("batch: %d %v", code, body)
	}
	batchID := body["batch_id"].(string)
	if code, _, _ = e.do(t, "POST", "/api/v1/batches/"+batchID+"/undo", e.dock, nil); code != 403 {
		t.Fatalf("undo needs write:*: %d", code)
	}
	code, body, _ = e.do(t, "POST", "/api/v1/batches/"+batchID+"/undo", e.admin, nil)
	if code != 200 || body["reverted"] != float64(2) {
		t.Fatalf("undo: %d %v", code, body)
	}
	code, body, _ = e.do(t, "GET", "/api/v1/changes?since=0&kinds=dock_*", e.narrow, nil)
	if code != 200 || len(body["items"].([]any)) != 4 {
		t.Fatalf("changes feed: %d %v", code, body)
	}
	huge := map[string]any{"title": "Big", "content": strings.Repeat("x", 2<<20), "kind": "dock_task"}
	if code, body, _ = e.do(t, "POST", "/api/v1/records", e.dock, huge); code != 413 || errCode(body) != "payload_too_large" {
		t.Fatalf("an oversized body is refused: %d %v", code, body)
	}
	code, body, _ = e.do(t, "POST", "/api/v1/code", e.admin, map[string]any{"script": `emit({"n": len(query(kind="dock_task")["items"])})`})
	if code != 200 || len(body["emitted"].([]any)) != 1 {
		t.Fatalf("code endpoint: %d %v", code, body)
	}
}

func TestStreamDeliversChangesAcrossWriters(t *testing.T) {
	e := setup(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/stream?since=0&kinds=dock_*", nil)
	req.Header.Set("Authorization", "Bearer "+e.narrow)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream headers: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				events <- sc.Text()[6:]
			}
		}
		close(events)
	}()
	time.Sleep(150 * time.Millisecond)
	// A write from outside the API, as an agent running the CLI would make.
	if _, err := e.store.SaveArtifactWith(context.Background(), store.SaveInput{Title: "Streamed", Content: "c", Kind: "dock_task", Fields: map[string]any{"status": "todo"}}, store.WriteOptions{Actor: "cli"}); err != nil {
		t.Fatal(err)
	}
	mustSave(t, e.store, "Not for this token", "result")
	select {
	case ev := <-events:
		var c store.Change
		if err := json.Unmarshal([]byte(ev), &c); err != nil || c.Kind != "dock_task" || c.Op != "create" {
			t.Fatalf("event: %s %v", ev, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2 seconds")
	}
	select {
	case ev := <-events:
		t.Fatalf("the memory record is outside the token's scope and must not stream: %s", ev)
	case <-time.After(300 * time.Millisecond):
	}
}
