package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/paulwritescode/lighthouse/internal/store"
	"github.com/paulwritescode/lighthouse/internal/transfer"
)

//go:embed templates/*.html static/* static/fonts/*
var assets embed.FS

type Server struct {
	store  *store.Store
	dbPath string
	csrf   string
	tmpl   *template.Template
}

type pageData struct {
	Page           string
	Title          string
	Query          string
	Project        string
	Error          string
	DBPath         string
	DBSize         string
	DBWALSize      string
	DBTotalSize    string
	Counts         store.Counts
	Artifacts      []store.Artifact
	Memories       []store.Memory
	RecentMemories []store.Memory
	Artifact       *store.Artifact
	CSRF           string
	Changes        []store.ChangeEvent
	Activity       []store.ActivityDay
	SecurityStatus string
	FieldsJSON     string
	Notice         string
	SelectAll      bool
	BulkAll        bool
}

func New(s *store.Store, dbPath string) (*Server, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find Lighthouse config directory: %w", err)
	}
	return newWithCSRFPath(s, dbPath, filepath.Join(configDir, "lighthouse", "csrf-token"))
}

func newWithCSRFPath(s *store.Store, dbPath, csrfPath string) (*Server, error) {
	funcs := template.FuncMap{"date": func(t time.Time) string { return t.Local().Format("Jan 2, 2006 · 3:04 PM") }, "short": func(v string, n int) string {
		if len(v) <= n {
			return v
		}
		return v[:n] + "…"
	}}
	t, err := template.New("page.html").Funcs(funcs).ParseFS(assets, "templates/page.html")
	if err != nil {
		return nil, err
	}
	token, err := loadCSRFToken(csrfPath)
	if err != nil {
		return nil, err
	}
	return &Server{store: s, dbPath: dbPath, csrf: token, tmpl: t}, nil
}

func loadCSRFToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 64 {
			return "", fmt.Errorf("invalid Lighthouse CSRF token file %q", path)
		}
		if _, err := hex.DecodeString(string(data)); err != nil {
			return "", fmt.Errorf("invalid Lighthouse CSRF token file %q: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("secure Lighthouse CSRF token file: %w", err)
		}
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read Lighthouse CSRF token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create Lighthouse config directory: %w", err)
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate Lighthouse CSRF token: %w", err)
	}
	value := hex.EncodeToString(token[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return loadCSRFToken(path)
	}
	if err != nil {
		return "", fmt.Errorf("create Lighthouse CSRF token: %w", err)
	}
	if _, err := file.WriteString(value); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write Lighthouse CSRF token: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("sync Lighthouse CSRF token: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close Lighthouse CSRF token: %w", err)
	}
	return value, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.home)
	mux.HandleFunc("GET /artifacts", s.artifacts)
	mux.HandleFunc("GET /artifacts/{id}", s.artifact)
	mux.HandleFunc("GET /memory", s.memory)
	mux.HandleFunc("GET /export", s.exportData)
	mux.HandleFunc("GET /artifacts/new", s.newArtifact)
	mux.HandleFunc("GET /artifacts/{id}/edit", s.editArtifact)
	mux.HandleFunc("GET /memory/new", s.newMemory)
	mux.HandleFunc("POST /artifacts", s.createArtifact)
	mux.HandleFunc("POST /artifacts/{id}/update", s.updateArtifact)
	mux.HandleFunc("POST /artifacts/{id}/delete", s.deleteArtifact)
	mux.HandleFunc("POST /artifacts/bulk", s.bulkArtifacts)
	mux.HandleFunc("POST /memory", s.createMemory)
	mux.HandleFunc("POST /memory/{id}/update", s.updateMemory)
	mux.HandleFunc("POST /memory/{id}/delete", s.deleteMemory)
	mux.HandleFunc("POST /memory/bulk", s.bulkMemory)
	mux.HandleFunc("POST /import", s.importData)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(mustSub(assets, "static"))))
	return s.localOnly(mux)
}

func (s *Server) render(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	d.CSRF = s.csrf
	http.SetCookie(w, &http.Cookie{Name: "lighthouse_csrf", Value: s.csrf, Path: "/", HttpOnly: true, Secure: false, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "page.html", d); err != nil {
		return
	}
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := pageData{Page: "home", Title: "Overview", DBPath: s.dbPath}
	d.DBSize, d.DBWALSize, d.DBTotalSize = databaseFootprint(s.dbPath)
	d.Counts, _ = s.store.Counts(ctx)
	d.Changes, _ = s.store.RecentChanges(ctx, 8)
	d.Activity, _ = s.store.WeeklyActivity(ctx)
	d.SecurityStatus = "Local-only · Loopback bound · CSRF protected"
	d.Artifacts, _ = s.store.Recent(ctx, "", 8)
	d.RecentMemories, _ = s.store.ListMemory(ctx, "", 6)
	s.render(w, http.StatusOK, d)
}

func databaseFootprint(path string) (mainSize, walSize, totalSize string) {
	if path == ":memory:" || path == "" {
		return "In memory", "—", "In memory"
	}
	main := fileSize(path)
	wal := fileSize(path + "-wal")
	return formatBytes(main), formatBytes(wal), formatBytes(main + wal)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(n)
	for _, unit := range units {
		size /= 1024
		if size < 1024 {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%.1f PiB", size/1024)
}
func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	items, err := s.store.Search(r.Context(), q, project, 50)
	d := pageData{Page: "library", Title: "Library", Query: q, Project: project, Artifacts: items, DBPath: s.dbPath, SelectAll: r.URL.Query().Get("select") == "all", BulkAll: r.URL.Query().Get("select") == "matching"}
	d.Counts, _ = s.store.Counts(r.Context())
	d.Notice = r.URL.Query().Get("notice")
	if err != nil {
		d.Error = err.Error()
	}
	s.render(w, http.StatusOK, d)
}
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d := pageData{Page: "artifact", Title: a.Title, Artifact: &a, DBPath: s.dbPath}
	fields, _ := json.MarshalIndent(a.Fields, "", "  ")
	d.FieldsJSON = string(fields)
	d.Counts, _ = s.store.Counts(r.Context())
	s.render(w, http.StatusOK, d)
}
func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	items, err := s.store.ListMemory(r.Context(), project, 100)
	d := pageData{Page: "memory", Title: "Memory", Project: project, Memories: items, DBPath: s.dbPath, SelectAll: r.URL.Query().Get("select") == "all", BulkAll: r.URL.Query().Get("select") == "matching"}
	d.Counts, _ = s.store.Counts(r.Context())
	if err != nil {
		d.Error = err.Error()
	}
	s.render(w, http.StatusOK, d)
}

func (s *Server) localOnly(next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "lighthouse": true, "lighthouse.localhost": true, "127.0.0.1": true, "::1": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if !allowed[strings.ToLower(host)] {
			http.Error(w, "This local dashboard only accepts Lighthouse loopback hostnames.", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "GET, HEAD, POST")
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !s.validMutation(r) {
				http.Error(w, "Request rejected: Lighthouse requires a same-origin form with a valid CSRF token. Refresh the page and try again.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validMutation(r *http.Request) bool {
	cookie, err := r.Cookie("lighthouse_csrf")
	if err != nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.csrf)) != 1 {
		return false
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		err = r.ParseMultipartForm(16 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(s.csrf)) != 1 {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" && !strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://"), r.Host) {
		return false
	}
	return true
}

func (s *Server) redirectNotice(w http.ResponseWriter, r *http.Request, path, message string) {
	http.Redirect(w, r, path+"?notice="+urlQuery(message), http.StatusSeeOther)
}
func urlQuery(v string) string {
	return url.QueryEscape(v)
}
func (s *Server) requireConfirm(r *http.Request) error {
	if r.FormValue("confirm") != "yes" {
		return errors.New("confirm this destructive action before deleting")
	}
	return nil
}
func (s *Server) newArtifact(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, pageData{Page: "new-artifact", Title: "New result", DBPath: s.dbPath, FieldsJSON: "{}"})
}
func (s *Server) editArtifact(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, _ := json.MarshalIndent(a.Fields, "", "  ")
	s.render(w, http.StatusOK, pageData{Page: "edit-artifact", Title: "Edit result", DBPath: s.dbPath, Artifact: &a, FieldsJSON: string(b)})
}
func (s *Server) createArtifact(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	fields, err := parseFields(r.FormValue("fields"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a, err := s.store.SaveArtifactFields(r.Context(), r.FormValue("title"), r.FormValue("content"), r.FormValue("project"), "user", r.FormValue("kind"), fields, "dashboard")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.redirectNotice(w, r, "/artifacts/"+a.ID, "Result created")
}
func (s *Server) updateArtifact(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	fields, err := parseFields(r.FormValue("fields"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	patch := map[string]any{"title": r.FormValue("title"), "content": r.FormValue("content"), "project": r.FormValue("project"), "agent": r.FormValue("agent"), "kind": r.FormValue("kind")}
	for k, v := range fields {
		patch[k] = v
	}
	a, err := s.store.UpdateArtifact(r.Context(), r.PathValue("id"), patch, "dashboard")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.redirectNotice(w, r, "/artifacts/"+a.ID, "Result updated")
}
func (s *Server) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	if err := s.requireConfirm(r); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	err := s.store.DeleteArtifact(r.Context(), r.PathValue("id"), "dashboard")
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.redirectNotice(w, r, "/artifacts", "Result deleted")
}

func (s *Server) bulkArtifacts(w http.ResponseWriter, r *http.Request) {
	ids := uniqueIDs(r.PostForm["ids"])
	if r.FormValue("all_matching") == "yes" {
		var err error
		ids, err = s.store.ArtifactIDs(r.Context(), r.FormValue("query"), r.FormValue("project_filter"))
		if err != nil {
			http.Error(w, "could not select matching results", http.StatusInternalServerError)
			return
		}
	}
	if len(ids) == 0 || len(ids) > 10000 {
		http.Error(w, "select between 1 and 10,000 results; narrow the filter for a larger set", http.StatusBadRequest)
		return
	}
	if r.FormValue("action") == "delete" {
		if err := s.requireConfirm(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		deleted := 0
		for _, id := range ids {
			if err := s.store.DeleteArtifact(r.Context(), id, "dashboard"); err != nil {
				s.redirectNotice(w, r, "/artifacts", fmt.Sprintf("Deleted %d results; stopped at an error: %s", deleted, err))
				return
			}
			deleted++
		}
		s.redirectNotice(w, r, "/artifacts", fmt.Sprintf("Deleted %d results", deleted))
		return
	}
	if r.FormValue("action") != "update" {
		http.Error(w, "choose update or delete", http.StatusBadRequest)
		return
	}
	patch := map[string]any{}
	if r.FormValue("update_project") == "yes" {
		patch["project"] = r.FormValue("project")
	}
	if r.FormValue("update_kind") == "yes" {
		patch["kind"] = r.FormValue("kind")
	}
	if r.FormValue("update_agent") == "yes" {
		patch["agent"] = r.FormValue("agent")
	}
	if len(patch) == 0 {
		http.Error(w, "select at least one shared field to update", http.StatusBadRequest)
		return
	}
	updated := 0
	for _, id := range ids {
		if _, err := s.store.UpdateArtifact(r.Context(), id, patch, "dashboard"); err != nil {
			s.redirectNotice(w, r, "/artifacts", fmt.Sprintf("Updated %d results; stopped at an error: %s", updated, err))
			return
		}
		updated++
	}
	s.redirectNotice(w, r, "/artifacts", fmt.Sprintf("Updated %d results", updated))
}

func (s *Server) bulkMemory(w http.ResponseWriter, r *http.Request) {
	ids := uniqueIDs(r.PostForm["ids"])
	if r.FormValue("all_matching") == "yes" {
		var err error
		ids, err = s.store.MemoryIDs(r.Context(), r.FormValue("project_filter"))
		if err != nil {
			http.Error(w, "could not select matching memory", http.StatusInternalServerError)
			return
		}
	}
	if len(ids) == 0 || len(ids) > 10000 {
		http.Error(w, "select between 1 and 10,000 memory items; narrow the filter for a larger set", http.StatusBadRequest)
		return
	}
	if r.FormValue("action") == "delete" {
		if err := s.requireConfirm(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		deleted := 0
		for _, id := range ids {
			if err := s.store.DeleteMemory(r.Context(), id, "dashboard"); err != nil {
				s.redirectNotice(w, r, "/memory", fmt.Sprintf("Deleted %d memory items; stopped at an error: %s", deleted, err))
				return
			}
			deleted++
		}
		s.redirectNotice(w, r, "/memory", fmt.Sprintf("Deleted %d memory items", deleted))
		return
	}
	if r.FormValue("action") != "update" {
		http.Error(w, "choose update or delete", http.StatusBadRequest)
		return
	}
	updateProject, updateKind := r.FormValue("update_project") == "yes", r.FormValue("update_kind") == "yes"
	if !updateProject && !updateKind {
		http.Error(w, "select project or kind to update", http.StatusBadRequest)
		return
	}
	updated := 0
	for _, id := range ids {
		item, err := s.store.GetMemory(r.Context(), id)
		if err != nil {
			s.redirectNotice(w, r, "/memory", fmt.Sprintf("Updated %d memory items; one selected item is no longer available", updated))
			return
		}
		project, kind := item.Project, item.Kind
		if updateProject {
			project = r.FormValue("project")
		}
		if updateKind {
			kind = r.FormValue("kind")
		}
		if err := s.store.UpdateMemory(r.Context(), item.ID, item.Key, item.Body, project, kind, item.SourceID, "dashboard"); err != nil {
			s.redirectNotice(w, r, "/memory", fmt.Sprintf("Updated %d memory items; stopped at an error: %s", updated, err))
			return
		}
		updated++
	}
	s.redirectNotice(w, r, "/memory", fmt.Sprintf("Updated %d memory items", updated))
}

func uniqueIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
func (s *Server) newMemory(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, pageData{Page: "new-memory", Title: "New memory", DBPath: s.dbPath})
}
func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	_, err := s.store.SaveMemory(r.Context(), r.FormValue("key"), r.FormValue("body"), r.FormValue("project"), r.FormValue("kind"), r.FormValue("source_id"), "dashboard")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.redirectNotice(w, r, "/memory", "Memory created")
}
func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if err := s.store.UpdateMemory(r.Context(), r.PathValue("id"), r.FormValue("key"), r.FormValue("body"), r.FormValue("project"), r.FormValue("kind"), r.FormValue("source_id"), "dashboard"); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.redirectNotice(w, r, "/memory", "Memory updated")
}
func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.requireConfirm(r); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.store.DeleteMemory(r.Context(), r.PathValue("id"), "dashboard"); err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	s.redirectNotice(w, r, "/memory", "Memory deleted")
}
func parseFields(raw string) (map[string]any, error) {
	var fields map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("custom fields must be a JSON object: %w", err)
	}
	if fields == nil {
		return nil, errors.New("custom fields must be a JSON object")
	}
	return fields, nil
}

func (s *Server) exportData(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	var data []byte
	var contentType, ext string
	if format == "db" {
		tmp, err := os.CreateTemp("", "lighthouse-export-*.db")
		if err != nil {
			http.Error(w, "could not create database snapshot", 500)
			return
		}
		path := tmp.Name()
		tmp.Close()
		os.Remove(path)
		defer os.Remove(path)
		if err = s.store.BackupTo(r.Context(), path); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="lighthouse-export.db"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		_, _ = io.Copy(w, f)
		return
	} else {
		a, err := s.store.AllArtifacts(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		m, err := s.store.AllMemory(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		data, contentType, ext, err = transfer.Export(format, a, m)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\"lighthouse-export."+ext+"\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
func (s *Server) importData(w http.ResponseWriter, r *http.Request) {
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "choose a file to import", 400)
		return
	}
	defer file.Close()
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
	var dataset transfer.Dataset
	if format == "db" {
		tmp, err := os.CreateTemp("", "lighthouse-import-*.db")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		path := tmp.Name()
		defer os.Remove(path)
		if _, err = io.Copy(tmp, file); err != nil {
			tmp.Close()
			http.Error(w, err.Error(), 500)
			return
		}
		if err = tmp.Close(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		dataset.Artifacts, dataset.Memories, err = store.ReadDatabase(r.Context(), path)
	} else {
		limit := int64(16 << 20)
		if format == "pdf" {
			limit = 1_400 << 10
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, limit+1))
		if readErr != nil {
			http.Error(w, "could not read import file", 400)
			return
		}
		if int64(len(raw)) > limit {
			http.Error(w, "import file exceeds the size limit for this format", 413)
			return
		}
		dataset, err = transfer.Import(format, raw)
	}
	if err != nil {
		http.Error(w, "import rejected: "+err.Error(), 400)
		return
	}
	a, err := s.store.ImportArtifacts(r.Context(), dataset.Artifacts)
	if err != nil {
		http.Error(w, "could not import results: "+err.Error(), 400)
		return
	}
	m, err := s.store.ImportMemories(r.Context(), dataset.Memories)
	if err != nil {
		http.Error(w, "could not import memory: "+err.Error(), 400)
		return
	}
	s.redirectNotice(w, r, "/", fmt.Sprintf("Imported %d results and %d memory items", a, m))
}
func mustSub(fsys fs.FS, dir string) fs.FS {
	f, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return f
}
