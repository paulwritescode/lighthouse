package web

import (
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
