package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/haruotsu/marunage/internal/skills/registry"
)

// upstreamFetchTimeout caps how long the Web UI waits on the
// registry catalog fetch before giving up. Keeps a hung publisher
// from stalling browser dashboards indefinitely.
const upstreamFetchTimeout = 10 * time.Second

// SkillsConfig wires the read-side skill registry surface into the
// Web UI. Both fields are optional: a zero SkillsConfig disables the
// `/skills` and `/api/skills/registry` routes entirely so PR-62's
// existing tests continue to see the same surface.
type SkillsConfig struct {
	// SkillsRoot is the absolute path the registry CLI installs
	// under (~/.claude/skills). When empty, the handlers report an
	// empty installed list rather than reaching into the user's
	// real ~/.claude/skills.
	SkillsRoot string
	// RegistryURL is the base URL of the shared registry. When
	// empty, /api/skills/registry returns a 503 so the UI can
	// render a "configure your registry" hint.
	RegistryURL string
	// Client lets tests inject a fake transport for the registry
	// fetcher. Production code leaves it nil and relies on the
	// default *http.Client built inside registry.Client.
	Client registry.Doer

	// AllowInsecure mirrors registry.Client.AllowInsecure: production
	// keeps it false (https-only), tests with httptest set true.
	AllowInsecure bool
}

// installedSkillsResponse is the JSON shape the `/api/skills/installed`
// endpoint returns. Keeping it exported-but-unstable means a future
// dashboard PR can grow the schema without breaking the Web UI's
// internal contract.
type installedSkillsResponse struct {
	Skills []registry.InstalledSkill `json:"skills"`
}

// registryResponse is the JSON shape returned from
// `/api/skills/registry`. The wrapper mirrors installedSkillsResponse
// so the dashboard can iterate both lists with the same template.
type registryResponse struct {
	Skills []registry.IndexEntry `json:"skills"`
}

// newSkillsHandler returns the GET /skills HTML page that lists
// every entry in <SkillsRoot>/.marunage-registry.json. It is a thin
// read-only surface so users can see what their registry-installed
// skills look like without leaving the dashboard.
func newSkillsHandler(renderer Renderer, csrf *CSRF, cfg SkillsConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := csrf.TokenFor(w, r); err != nil {
			http.Error(w, "csrf token issue failed", http.StatusInternalServerError)
			return
		}
		state, err := loadInstalled(cfg.SkillsRoot)
		if err != nil {
			http.Error(w, fmt.Sprintf("skills: %v", err), http.StatusInternalServerError)
			return
		}
		data := struct {
			Installed   []registry.InstalledSkill
			RegistryURL string
		}{Installed: state.Installed, RegistryURL: cfg.RegistryURL}
		if err := renderer.Render(w, "skills.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

// newInstalledSkillsAPIHandler exposes the read-only JSON view of
// the on-disk state file for the dashboard to consume via fetch().
// Mutating endpoints are intentionally absent from PR-203 — install /
// update remain CLI-only until a follow-up PR lands a CSRF-aware
// POST surface.
func newInstalledSkillsAPIHandler(cfg SkillsConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		state, err := loadInstalled(cfg.SkillsRoot)
		if err != nil {
			http.Error(w, fmt.Sprintf("skills: %v", err), http.StatusInternalServerError)
			return
		}
		installed := state.Installed
		if installed == nil {
			installed = []registry.InstalledSkill{}
		}
		writeJSON(w, http.StatusOK, installedSkillsResponse{Skills: installed})
	})
}

// newRegistrySearchAPIHandler proxies the registry index through the
// Web UI so the dashboard can render a catalog without baking the
// upstream URL into the front-end. The handler returns 503 when no
// registry is configured so the UI can render an actionable hint.
func newRegistrySearchAPIHandler(cfg SkillsConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.RegistryURL == "" {
			http.Error(w, "skills: registry URL not configured", http.StatusServiceUnavailable)
			return
		}
		client := &registry.Client{
			BaseURL:       cfg.RegistryURL,
			HTTPClient:    cfg.Client,
			UserAgent:     "marunage-web",
			AllowInsecure: cfg.AllowInsecure,
		}
		ctx, cancel := context.WithTimeout(r.Context(), upstreamFetchTimeout)
		defer cancel()
		idx, err := client.FetchIndex(ctx)
		if err != nil {
			http.Error(w, fmt.Sprintf("skills: registry: %v", err), http.StatusBadGateway)
			return
		}
		hits := registry.Search(idx, r.URL.Query().Get("q"))
		writeJSON(w, http.StatusOK, registryResponse{Skills: hits})
	})
}

// loadInstalled is the small helper both /skills and
// /api/skills/installed share. Centralising it ensures both surfaces
// see the exact same on-disk state and treat a missing root as the
// empty case.
func loadInstalled(root string) (registry.State, error) {
	if root == "" {
		return registry.State{SchemaVersion: registry.SchemaVersion}, nil
	}
	return registry.LoadState(root)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// Body has already been partially written; the client will
		// see a truncated response. Log via http.Error is not
		// possible after WriteHeader, so just give up silently.
		_ = err
	}
}
