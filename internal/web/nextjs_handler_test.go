package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedNextJSDir creates a minimal Next.js static export layout in a temp
// directory so tests exercise the same fs.Stat behaviour as embed.FS and
// os.DirFS (both return "invalid argument" for paths with trailing slashes,
// unlike fstest.MapFS which returns ErrNotExist).
func seedNextJSDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html":                  "<html>index</html>",
		"skills/index.html":           "<html>skills</html>",
		"_next/static/chunks/app.css": "body{color:red}",
		"_next/static/chunks/app.js":  "console.log('ok')",
	}
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// TestNextJSHandler_TrailingSlashFallsBackToIndexHTML guards against
// the regression where fs.Stat("skills/") returns "invalid argument"
// (not ErrNotExist) on os.DirFS / embed.FS, causing a 500 instead of
// the SPA index.html fallback.
func TestNextJSHandler_TrailingSlashFallsBackToIndexHTML(t *testing.T) {
	h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
	req := httptest.NewRequest("GET", "/skills/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /skills/ status = %d; want 200 (not 500)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "index") {
		t.Errorf("GET /skills/ body should be index.html fallback; got %q", rec.Body.String())
	}
}

func TestNextJSHandler_MissingFileFallsBackToIndexHTML(t *testing.T) {
	h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
	req := httptest.NewRequest("GET", "/nonexistent", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /nonexistent status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "index") {
		t.Errorf("GET /nonexistent body should be index.html fallback; got %q", rec.Body.String())
	}
}

func TestNextJSHandler_StaticFileServesWithCorrectMIME(t *testing.T) {
	h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
	req := httptest.NewRequest("GET", "/_next/static/chunks/app.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /_next/static/chunks/app.css status = %d; want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q; want text/css", ct)
	}
}

func TestNextJSHandler_RootServesIndexHTML(t *testing.T) {
	h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "index") {
		t.Errorf("GET / body should be index.html; got %q", rec.Body.String())
	}
}
