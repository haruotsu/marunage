package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newSkillsServer wires a Server with a tempdir-backed SkillsRoot
// and the test-only CSRF source. Centralising the helper keeps each
// test focused on one behaviour.
func newSkillsServer(t *testing.T, cfg SkillsConfig) *Server {
	t.Helper()
	srv, err := NewServer(Options{
		TokenSource:       testTokenSource,
		HeartbeatInterval: 25 * time.Millisecond,
		EnableTestRoutes:  true,
		Skills:            cfg,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

// seedStateFile drops a fixture .marunage-registry.json under root so
// the read paths have something concrete to render.
func seedStateFile(t *testing.T, root string, entries []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"installed":      entries,
	})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".marunage-registry.json"), body, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
}

// TestRoutes_SkillsHTML_RendersInstalledSkills pins the read-side
// dashboard placeholder: GET /skills returns HTML containing every
// installed skill name from the on-disk state file.
func TestRoutes_SkillsHTML_RendersInstalledSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude", "skills")
	seedStateFile(t, root, []map[string]any{
		{"name": "marunage-source-jira", "version": "0.1.0", "source": "https://example"},
	})

	srv := newSkillsServer(t, SkillsConfig{SkillsRoot: root})
	rec := doGet(t, srv, "/skills")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q; want text/html prefix", ct)
	}
	if !strings.Contains(rec.Body.String(), "marunage-source-jira") {
		t.Errorf("body missing skill name; body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "0.1.0") {
		t.Errorf("body missing version; body=%s", rec.Body.String())
	}
}

// TestRoutes_SkillsHTML_EmptyState pins the empty-state UX so a
// brand-new install does not render a malformed table.
func TestRoutes_SkillsHTML_EmptyState(t *testing.T) {
	srv := newSkillsServer(t, SkillsConfig{SkillsRoot: t.TempDir()})
	rec := doGet(t, srv, "/skills")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No registry-installed skills") {
		t.Errorf("body missing empty-state hint; body=%s", rec.Body.String())
	}
}

// TestRoutes_SkillsInstalledAPI_ReturnsJSON pins the read-only
// JSON contract the dashboard fetch() can consume.
func TestRoutes_SkillsInstalledAPI_ReturnsJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude", "skills")
	seedStateFile(t, root, []map[string]any{
		{"name": "marunage-source-jira", "version": "0.1.0"},
	})

	srv := newSkillsServer(t, SkillsConfig{SkillsRoot: root})
	rec := doGet(t, srv, "/api/skills/installed")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
	var parsed installedSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Skills) != 1 || parsed.Skills[0].Name != "marunage-source-jira" {
		t.Errorf("response = %+v; want one jira entry", parsed)
	}
}

// TestRoutes_SkillsRegistryAPI_FetchesAndFilters pins the pass-
// through query: the handler hits the configured upstream registry
// and returns the matching index entries as JSON.
func TestRoutes_SkillsRegistryAPI_FetchesAndFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"schema_version": 1,
			"skills": [
				{"name": "marunage-source-jira", "latest": "0.1.0", "description": "Jira"},
				{"name": "marunage-source-linear", "latest": "0.1.0", "description": "Linear"}
			]
		}`))
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	srv := newSkillsServer(t, SkillsConfig{RegistryURL: upstream.URL, AllowInsecure: true})
	rec := doGet(t, srv, "/api/skills/registry?q=jira")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var parsed registryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Skills) != 1 || parsed.Skills[0].Name != "marunage-source-jira" {
		t.Errorf("response = %+v; want only jira entry", parsed)
	}
}

// TestRoutes_SkillsRegistryAPI_NotConfigured pins the actionable
// 503 when the registry URL is missing — the dashboard can render
// "configure your registry" rather than an opaque 500.
func TestRoutes_SkillsRegistryAPI_NotConfigured(t *testing.T) {
	srv := newSkillsServer(t, SkillsConfig{})
	rec := doGet(t, srv, "/api/skills/registry")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", rec.Code)
	}
}

// TestRoutes_SkillsAPI_DoesNotExposeMutatingMethods pins the
// "read-only in PR-203" promise. POST /api/skills/installed must
// 405 / 404 rather than mutate state.
func TestRoutes_SkillsAPI_DoesNotExposeMutatingMethods(t *testing.T) {
	srv := newSkillsServer(t, SkillsConfig{SkillsRoot: t.TempDir()})

	req := httptest.NewRequest(http.MethodPost, "/api/skills/installed", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("POST /api/skills/installed should not succeed; got %d", rec.Code)
	}
}
