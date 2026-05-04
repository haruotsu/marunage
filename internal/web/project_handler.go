package web

import (
	"context"
	"net/http"
	"net/url"
)

// ProjectItem is one item on the project board.
type ProjectItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// ProjectPhase is one phase (column) of the project board.
type ProjectPhase struct {
	Name   string        `json:"name"`
	Status string        `json:"status"`
	Items  []ProjectItem `json:"items"`
}

// ProjectSnapshot holds the board state for a GitHub project.
type ProjectSnapshot struct {
	Phases []ProjectPhase
}

// ProjectProvider is the seam the project handlers consume.
// boardURL is the GitHub Projects URL; empty means the default configured board.
type ProjectProvider interface {
	ProjectSnapshot(ctx context.Context, boardURL string) (ProjectSnapshot, error)
}

type noopProjectProvider struct{}

func (noopProjectProvider) ProjectSnapshot(_ context.Context, _ string) (ProjectSnapshot, error) {
	return ProjectSnapshot{Phases: []ProjectPhase{}}, nil
}

const projectLoadFailedMessage = "Project data unavailable. See daemon.log for details."

type projectAPIResponse struct {
	Phases []ProjectPhase `json:"phases"`
}

// projectPageData is the template payload for project.html.
type projectPageData struct {
	Phases    []ProjectPhase
	BoardURL  string
	LoadError string
}

// validateBoardURL returns false when boardURL is non-empty but uses a disallowed scheme.
// Only http and https are permitted; javascript:, data:, ftp:, file: etc. are rejected
// to prevent SSRF and XSS vectors.
func validateBoardURL(boardURL string) bool {
	if boardURL == "" {
		return true
	}
	u, err := url.Parse(boardURL)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return true
	default:
		return false
	}
}

// newProjectAPIHandler returns GET /api/project.
func newProjectAPIHandler(provider ProjectProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		boardURL := r.URL.Query().Get("board_url")
		if !validateBoardURL(boardURL) {
			writeJSONError(w, http.StatusBadRequest, "invalid board_url: only http and https schemes are allowed")
			return
		}
		snap, err := provider.ProjectSnapshot(r.Context(), boardURL)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "project data unavailable")
			return
		}
		phases := snap.Phases
		if phases == nil {
			phases = []ProjectPhase{}
		}
		writeJSON(w, http.StatusOK, projectAPIResponse{Phases: phases})
	})
}

// newProjectHandler returns GET /project HTML page.
func newProjectHandler(renderer Renderer, provider ProjectProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		boardURL := r.URL.Query().Get("board_url")
		if !validateBoardURL(boardURL) {
			http.Error(w, "invalid board_url: only http and https schemes are allowed", http.StatusBadRequest)
			return
		}
		snap, err := provider.ProjectSnapshot(r.Context(), boardURL)
		if err != nil {
			page := projectPageData{LoadError: projectLoadFailedMessage}
			if renderErr := renderer.Render(w, "project.html", page); renderErr != nil {
				http.Error(w, "render failed", http.StatusInternalServerError)
			}
			return
		}
		page := projectPageData{
			Phases:   snap.Phases,
			BoardURL: boardURL,
		}
		if renderErr := renderer.Render(w, "project.html", page); renderErr != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	})
}
