package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticProjectProvider struct {
	snap ProjectSnapshot
	err  error
}

func (s staticProjectProvider) ProjectSnapshot(_ context.Context, _ string) (ProjectSnapshot, error) {
	return s.snap, s.err
}

func newProjectServer(t *testing.T, prov ProjectProvider) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Project:           prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func sampleProjectSnapshot() ProjectSnapshot {
	return ProjectSnapshot{
		Phases: []ProjectPhase{
			{
				Name:   "Phase 1",
				Status: "done",
				Items:  []ProjectItem{{ID: "1", Title: "Task A", Status: "done"}},
			},
			{
				Name:   "Phase 2",
				Status: "in_progress",
				Items:  []ProjectItem{{ID: "2", Title: "Task B", Status: "in_progress"}},
			},
		},
	}
}

// 6. /api/project returns project board phases.
func TestProjectAPIHandler_ReturnsPhases(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	phases, ok := got["phases"].([]any)
	if !ok {
		t.Fatalf("phases missing or wrong type: %T", got["phases"])
	}
	if len(phases) != 2 {
		t.Errorf("phases len=%d; want 2", len(phases))
	}
}

// /api/project?board_url=... passes board_url to the provider.
func TestProjectAPIHandler_BoardURLPassedToProvider(t *testing.T) {
	prov := &captureProjectProvider{snap: sampleProjectSnapshot()}
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		Project:           prov,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/project?board_url=https://github.com/orgs/test/projects/1", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", w.Code)
	}
	if len(prov.boardURLs) == 0 {
		t.Fatal("ProjectSnapshot not called")
	}
	if prov.boardURLs[0] != "https://github.com/orgs/test/projects/1" {
		t.Errorf("board_url=%q; want https://github.com/orgs/test/projects/1", prov.boardURLs[0])
	}
}

type captureProjectProvider struct {
	snap      ProjectSnapshot
	boardURLs []string
}

func (c *captureProjectProvider) ProjectSnapshot(_ context.Context, boardURL string) (ProjectSnapshot, error) {
	c.boardURLs = append(c.boardURLs, boardURL)
	return c.snap, nil
}

// GET /project page returns 200.
func TestProjectHandler_Returns200(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type=%q; want text/html", ct)
	}
}

// /api/project returns 500 when provider errors.
func TestProjectAPIHandler_ProviderErrorReturns500(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{err: errProjectTestFailed})

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", w.Code)
	}
}

// /api/project sets Cache-Control: no-store.
func TestProjectAPIHandler_SetsCacheControlNoStore(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q; want no-store", got)
	}
}

// /api/project returns application/json content type.
func TestProjectAPIHandler_ReturnsJSON(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q; want application/json", ct)
	}
}

// /api/project?board_url=javascript:... returns 400 (SSRF / XSS prevention).
func TestProjectAPIHandler_InvalidBoardURLReturns400(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	for _, bad := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>",
		"ftp://example.com/board",
		"file:///etc/passwd",
	} {
		t.Run(bad, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/project?board_url="+bad, nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("board_url=%q: status=%d; want 400", bad, w.Code)
			}
		})
	}
}

// /api/project?board_url= (empty) is OK — means default board.
func TestProjectAPIHandler_EmptyBoardURLIsOK(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/project", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("empty board_url: status=%d; want 200", w.Code)
	}
}

// /api/project?board_url=https://... is OK.
func TestProjectAPIHandler_ValidBoardURLIsOK(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	req := httptest.NewRequest(http.MethodGet, "/api/project?board_url=https://github.com/orgs/test/projects/1", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("valid https board_url: status=%d; want 200", w.Code)
	}
}

// 7. CSRF: POST to new API endpoints without token returns 403.
func TestNewEndpoints_CSRFBlocksUnauthenticatedPOST(t *testing.T) {
	srv := newProjectServer(t, staticProjectProvider{snap: sampleProjectSnapshot()})

	for _, path := range []string{"/api/project", "/api/metrics", "/api/journal"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			w := httptest.NewRecorder()
			srv.Routes().ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("POST %s without CSRF: status=%d; want 403", path, w.Code)
			}
		})
	}
}

var errProjectTestFailed = projectTestSentinel("project provider test failure")

type projectTestSentinel string

func (e projectTestSentinel) Error() string { return string(e) }
