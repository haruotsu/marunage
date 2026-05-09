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
		"journal/index.html":          "<html>journal</html>",
		"metrics/index.html":          "<html>metrics</html>",
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

// TestNextJSHandler_TrailingSlashServesDirectoryIndex guards against the
// regression where fs.Stat("skills/") returns "invalid argument" (not
// ErrNotExist) on os.DirFS / embed.FS, causing a 500. It also verifies
// that a trailing slash serves the route-specific page, not the SPA root.
func TestNextJSHandler_TrailingSlashServesDirectoryIndex(t *testing.T) {
	h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
	req := httptest.NewRequest("GET", "/skills/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /skills/ status = %d; want 200 (not 500)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "skills") {
		t.Errorf("GET /skills/ body = %q; want route-specific page (skills/index.html)", rec.Body.String())
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

func TestNextJSHandler_DirectoryRouteServesDirectoryIndex(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/journal", "journal"},
		{"/metrics", "metrics"},
		// /skills without trailing slash; complements TrailingSlash test which uses /skills/
		{"/skills", "skills"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h := newNextJSHandler(os.DirFS(seedNextJSDir(t)))
			req := httptest.NewRequest("GET", tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d; want 200", tc.path, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("GET %s body = %q; want it to contain %q (route-specific page, not root index)", tc.path, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestNextJSHandler_DirectoryWithoutIndexFallsBackToSPARoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>index</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty-route"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := newNextJSHandler(os.DirFS(root))
	req := httptest.NewRequest("GET", "/empty-route", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /empty-route status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "index") {
		t.Errorf("GET /empty-route body = %q; want SPA root (index.html) fallback", rec.Body.String())
	}
}
