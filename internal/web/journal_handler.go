package web

import (
	"context"
	"net/http"
)

// JournalEntry is one activity entry in the work journal.
type JournalEntry struct {
	Time    string `json:"time"`
	Source  string `json:"source"`
	Summary string `json:"summary"`
}

// JournalSnapshot holds journal entries for a given date.
type JournalSnapshot struct {
	Date    string
	Entries []JournalEntry
}

// JournalProvider is the seam the journal handlers consume.
// date is the YYYY-MM-DD string to filter by; empty means today.
type JournalProvider interface {
	JournalSnapshot(ctx context.Context, date string) (JournalSnapshot, error)
}

type noopJournalProvider struct{}

func (noopJournalProvider) JournalSnapshot(_ context.Context, date string) (JournalSnapshot, error) {
	return JournalSnapshot{Date: date, Entries: []JournalEntry{}}, nil
}

const journalLoadFailedMessage = "Journal data unavailable. See daemon.log for details."

type journalAPIResponse struct {
	Entries []JournalEntry `json:"entries"`
}

// journalPageData is the template payload for journal.html.
type journalPageData struct {
	Date      string
	Entries   []JournalEntry
	LoadError string
}

// newJournalAPIHandler returns GET /api/journal.
func newJournalAPIHandler(provider JournalProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		date := r.URL.Query().Get("date")
		snap, err := provider.JournalSnapshot(r.Context(), date)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "journal data unavailable")
			return
		}
		entries := snap.Entries
		if entries == nil {
			entries = []JournalEntry{}
		}
		writeJSON(w, http.StatusOK, journalAPIResponse{Entries: entries})
	})
}

// newJournalHandler returns GET /journal HTML page.
func newJournalHandler(renderer Renderer, provider JournalProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		date := r.URL.Query().Get("date")
		snap, err := provider.JournalSnapshot(r.Context(), date)
		if err != nil {
			page := journalPageData{LoadError: journalLoadFailedMessage}
			if renderErr := renderer.Render(w, "journal.html", page); renderErr != nil {
				http.Error(w, "render failed", http.StatusInternalServerError)
			}
			return
		}
		page := journalPageData{
			Date:    snap.Date,
			Entries: snap.Entries,
		}
		if renderErr := renderer.Render(w, "journal.html", page); renderErr != nil {
			http.Error(w, "render failed", http.StatusInternalServerError)
		}
	})
}
