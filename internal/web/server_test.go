package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/paulwritescode/lighthouse/internal/store"
)

var csrfInputPattern = regexp.MustCompile(`<input type="hidden" name="csrf_token" value="([a-f0-9]{64})">`)

func TestArtifactMarkdownAndStructuredFieldsRenderReadably(t *testing.T) {
	outline := markdownOutline("## Preliminary actionable ranking\n\n### Source register")
	if len(outline) != 2 || outline[0].ID != "preliminary-actionable-ranking" || outline[0].Level != 2 || outline[1].ID != "source-register" || outline[1].Level != 3 {
		t.Fatalf("Markdown outline IDs/levels = %#v", outline)
	}
	shifted := string(renderArtifactMarkdown("## Research details\n\n### Subsection"))
	if !strings.Contains(shifted, `<h3 id="research-details">`) || !strings.Contains(shifted, `<h4 id="subsection">`) {
		t.Fatalf("artifact headings were not shifted below the record title: %s", shifted)
	}
	markdown := string(renderMarkdown("| Name | Score |\n| --- | ---: |\n| Alpha | 4 |\n\n| Status | Count |\n| --- | ---: |\n| Ready | 2 |\n\n[Source](https://example.com/research)"))
	for _, want := range []string{`<table>`, `<th scope="col">Name</th>`, `<th scope="col" style="text-align:right">Score</th>`, `<td>Alpha</td>`, `target="_blank" rel="noopener noreferrer"`} {
		if !strings.Contains(markdown, want) {
			t.Errorf("rendered Markdown missing %q: %s", want, markdown)
		}
	}
	if strings.Count(markdown, "<table>") != 2 || strings.Contains(markdown, "| Alpha | 4 |") {
		t.Fatalf("expected both tables to render without literal pipe rows: %s", markdown)
	}
	malformed := string(renderMarkdown("| Name | Score |\n| -- |\n| Alpha | 4 |"))
	if strings.Contains(malformed, "|") || !strings.Contains(malformed, "Name · Score") || !strings.Contains(malformed, "Alpha · 4") {
		t.Fatalf("malformed table did not get a readable fallback: %s", malformed)
	}

	fields := string(renderFields(map[string]any{
		"ranking": map[string]any{"score": float64(8), "notes": []any{"clear", "relevant"}},
		"metrics": map[string]any{"coverage": float64(92), "confidence": float64(.88)},
		"sources": []any{map[string]any{"title": "Instagram account", "type": "social_account", "url": "https://instagram.com/example", "attribution": "Public profile", "limitations": "Account, not a specific post"}, map[string]any{"title": "Local notes", "type": "local_file", "path": "/tmp/research/notes.md"}, map[string]any{"title": "Earlier result", "artifact_id": "d61212ddd19ad0072db5945d37012db6"}, map[string]any{"title": "Missing record"}},
		"unsafe":  `<script>alert(1)</script>`,
	}))
	for _, want := range []string{"ranking", "score", "clear", "artifact-metrics", "Instagram account", `href="https://instagram.com/example" target="_blank" rel="noopener noreferrer"`, "Account, not a specific post", `data-copy-path="/tmp/research/notes.md"`, "Copy path", "This local file cannot be opened in the browser", `href="/artifacts/d61212ddd19ad0072db5945d37012db6"`, "This source has no openable locator", "&lt;script&gt;"} {
		if !strings.Contains(fields, want) {
			t.Errorf("rendered custom fields missing %q: %s", want, fields)
		}
	}
	if strings.Contains(fields, "map[") {
		t.Fatal("custom fields included a Go map representation")
	}
}

func TestArtifactDetailUsesReadableMarkdownAndCustomFieldRenderers(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/lighthouse.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	artifact, err := db.SaveArtifactFields(t.Context(), "Research review", "## Research review\n\n| Topic | Score |\n| --- | ---: |\n| Result | 9 |", "research", "test", "result", map[string]any{
		"ranking": map[string]any{"score": float64(9)},
		"sources": []any{map[string]any{"title": "Source profile", "url": "https://example.com/profile", "type": "social_account", "limitations": "Profile link, not a specific post"}, map[string]any{"title": "Local evidence", "path": "/tmp/evidence.md", "type": "local_file"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := newWithCSRFPath(db, "test.db", t.TempDir()+"/csrf-token")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8080/artifacts/"+artifact.ID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET artifact detail status = %d", response.Code)
	}
	for _, want := range []string{`<nav class="artifact-outline"`, `href="#research-review"`, `<h3 id="research-review">`, `<th scope="col">Topic</th>`, `<td>Result</td>`, `class="source-cards"`, "ranking", "Profile link, not a specific post", `data-copy-path="/tmp/evidence.md"`, "This local file cannot be opened in the browser"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("artifact detail missing %q", want)
		}
	}
	if strings.Contains(response.Body.String(), "map[") {
		t.Fatal("artifact detail rendered a Go map representation")
	}
}

func TestLoadCSRFTokenPersistsWithPrivatePermissions(t *testing.T) {
	path := t.TempDir() + "/config/csrf-token"
	first, err := loadCSRFToken(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadCSRFToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || first != second {
		t.Fatal("CSRF token did not remain stable across server initialization")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("CSRF token file permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestDashboardPagesGroupExistingProjectRecords(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/lighthouse.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SaveArtifact(t.Context(), "Release review", "Recorded result", "dashboard-test", "codex", "result"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMemory(t.Context(), "review.spacing", "Keep table controls aligned", "dashboard-test", "fact", "", "test"); err != nil {
		t.Fatal(err)
	}
	server, err := newWithCSRFPath(db, "test.db", t.TempDir()+"/csrf-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/", "/artifacts", "/memory", "/projects", "/activity", "/data"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://localhost:8080"+route, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", route, response.Code)
		}
		if !strings.Contains(response.Body.String(), `data-theme="light"`) {
			t.Fatalf("GET %s is missing the light default theme", route)
		}
	}
	projects := httptest.NewRecorder()
	server.Handler().ServeHTTP(projects, httptest.NewRequest(http.MethodGet, "http://localhost:8080/projects", nil))
	for _, value := range []string{"dashboard-test", "Results", "Memory", ">2</td>"} {
		if !strings.Contains(projects.Body.String(), value) {
			t.Errorf("project summary missing %q", value)
		}
	}
}

func TestBulkArtifactFormCSRFAllowsSameOriginNoOp(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/lighthouse.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.SaveArtifact(t.Context(), "CSRF fixture", "No sensitive content.", "test", "test", "result"); err != nil {
		t.Fatal(err)
	}

	csrfPath := t.TempDir() + "/csrf-token"
	server, err := newWithCSRFPath(db, "test.db", csrfPath)
	if err != nil {
		t.Fatal(err)
	}
	oldHandler := server.Handler()

	get := httptest.NewRequest(http.MethodGet, "http://localhost:8080/artifacts", nil)
	getResponse := httptest.NewRecorder()
	oldHandler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET /artifacts status = %d, want %d", getResponse.Code, http.StatusOK)
	}

	match := csrfInputPattern.FindStringSubmatch(getResponse.Body.String())
	if len(match) != 2 {
		t.Fatal("/artifacts did not render a 64-character CSRF token")
	}
	var csrfCookie *http.Cookie
	for _, cookie := range getResponse.Result().Cookies() {
		if cookie.Name == "lighthouse_csrf" {
			csrfCookie = cookie
			break
		}
	}
	if csrfCookie == nil || csrfCookie.Value != match[1] {
		t.Fatal("CSRF cookie does not match the bulk form token")
	}

	// A service restart must preserve the token so a tab left open during an
	// update can still submit its same-origin form.
	restarted, err := newWithCSRFPath(db, "test.db", csrfPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://localhost:8080", "null"} {
		t.Run(origin, func(t *testing.T) {
			form := url.Values{"csrf_token": {match[1]}}
			post := httptest.NewRequest(http.MethodPost, "http://localhost:8080/artifacts/bulk", strings.NewReader(form.Encode()))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			post.Header.Set("Origin", origin)
			post.AddCookie(csrfCookie)
			postResponse := httptest.NewRecorder()
			restarted.Handler().ServeHTTP(postResponse, post)
			body, err := io.ReadAll(postResponse.Result().Body)
			if err != nil {
				t.Fatal(err)
			}
			if postResponse.Code != http.StatusBadRequest || !strings.Contains(string(body), "select between 1 and 10,000 results") {
				t.Fatalf("safe no-op POST status/body = %d/%q, want the bulk handler's selection validation", postResponse.Code, body)
			}
		})
	}
}

func TestBulkArtifactFormCSRFRejectsMissingCookie(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/lighthouse.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	server, err := newWithCSRFPath(db, "test.db", t.TempDir()+"/csrf-token")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf_token": {server.csrf}}
	post := httptest.NewRequest(http.MethodPost, "http://localhost:8080/artifacts/bulk", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "http://localhost:8080")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, post)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "valid CSRF token") {
		t.Fatalf("POST without cookie status/body = %d/%q, want CSRF rejection", response.Code, response.Body.String())
	}
}

func TestBulkArtifactFormCSRFRejectsForeignOriginAndWrongToken(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/lighthouse.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server, err := newWithCSRFPath(db, "test.db", t.TempDir()+"/csrf-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, origin, token string
	}{
		{name: "foreign origin", origin: "https://example.com", token: server.csrf},
		{name: "wrong token with opaque origin", origin: "null", token: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"csrf_token": {tc.token}}
			post := httptest.NewRequest(http.MethodPost, "http://localhost:8080/artifacts/bulk", strings.NewReader(form.Encode()))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			post.Header.Set("Origin", tc.origin)
			post.AddCookie(&http.Cookie{Name: "lighthouse_csrf", Value: server.csrf})
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, post)
			if response.Code != http.StatusForbidden {
				t.Fatalf("POST status = %d, want CSRF rejection", response.Code)
			}
		})
	}
}

func TestDeleteDatabaseRequiresConfirmationAndRecreatesIt(t *testing.T) {
	path := t.TempDir() + "/lighthouse.db"
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveArtifact(t.Context(), "Before", "Old content", "test", "test", "result"); err != nil {
		t.Fatal(err)
	}
	server, err := newWithCSRFPath(db, path, t.TempDir()+"/csrf-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.store.Close() })
	authorized := 0
	server.authorizeReset = func(_ context.Context) error { authorized++; return nil }
	post := func(phrase string) *httptest.ResponseRecorder {
		form := url.Values{"csrf_token": {server.csrf}, "confirm": {"yes"}, "erase_phrase": {phrase}}
		req := httptest.NewRequest(http.MethodPost, "http://localhost:8080/data/erase", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://localhost:8080")
		req.AddCookie(&http.Cookie{Name: "lighthouse_csrf", Value: server.csrf})
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	if response := post("wrong"); response.Code != http.StatusBadRequest || authorized != 0 {
		t.Fatalf("invalid confirmation: %d, auth calls %d", response.Code, authorized)
	}
	if response := post("DELETE"); response.Code != http.StatusSeeOther || authorized != 1 {
		t.Fatalf("authorized delete: %d, auth calls %d, body %s", response.Code, authorized, response.Body.String())
	}
	counts, err := server.store.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Artifacts != 0 || counts.Changes != 0 {
		t.Fatalf("old data remains: %+v", counts)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh database missing: %v", err)
	}
}
