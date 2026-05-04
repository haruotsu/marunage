package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

const (
	liveStreamPollInterval = 1 * time.Second
	liveStreamPingInterval = 30 * time.Second
)

// WorkspaceStreamer reads terminal output from and sends text to a cmux
// workspace. The production adapter wraps cmux.Client; tests inject a fake.
type WorkspaceStreamer interface {
	ReadOutput(ctx context.Context, workspaceID string) (string, error)
	Send(ctx context.Context, workspaceID string, text string) error
}

// LiveStreamProvider maps a task ID to its cmux workspace ID.
// Returns store.ErrNotFound when the task has no associated workspace.
type LiveStreamProvider interface {
	WorkspaceIDForTask(ctx context.Context, taskID int64) (string, error)
}

// LiveStreamConfig wires the live-stream endpoints.  Zero-valued fields fall
// back to noop implementations so existing tests that do not care about live
// streaming keep passing without wiring a fake.
type LiveStreamConfig struct {
	Streamer WorkspaceStreamer
	Provider LiveStreamProvider
}

// noopWorkspaceStreamer always returns empty output and accepts sends silently.
type noopWorkspaceStreamer struct{}

func (noopWorkspaceStreamer) ReadOutput(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (noopWorkspaceStreamer) Send(_ context.Context, _ string, _ string) error {
	return nil
}

// noopLiveStreamProvider always returns ErrNotFound so the handler returns 404
// for every task ID when no real provider is wired.
type noopLiveStreamProvider struct{}

func (noopLiveStreamProvider) WorkspaceIDForTask(_ context.Context, _ int64) (string, error) {
	return "", fmt.Errorf("live stream: %w", store.ErrNotFound)
}

// sendToWorkspaceRequest is the JSON body for POST /api/tasks/{id}/send.
type sendToWorkspaceRequest struct {
	Text string `json:"text"`
}

// newLiveStreamHandler returns GET /api/tasks/{id}/stream.
// It streams cmux pane-text output as SSE, polling liveStreamPollInterval
// and emitting an "output" event whenever the text changes.
func newLiveStreamHandler(streamer WorkspaceStreamer, provider LiveStreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}

		workspaceID, err := provider.WorkspaceIDForTask(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "workspace not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
		flusher.Flush()

		var lastOutput string
		pollTicker := time.NewTicker(liveStreamPollInterval)
		defer pollTicker.Stop()
		pingTicker := time.NewTicker(liveStreamPingInterval)
		defer pingTicker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-pingTicker.C:
				fmt.Fprintf(w, "event: ping\ndata: %d\n\n", time.Now().UnixMilli())
				flusher.Flush()
			case <-pollTicker.C:
				output, readErr := streamer.ReadOutput(r.Context(), workspaceID)
				if readErr != nil {
					continue
				}
				if output != lastOutput {
					lastOutput = output
					encoded, _ := json.Marshal(output)
					fmt.Fprintf(w, "event: output\ndata: %s\n\n", encoded)
					flusher.Flush()
				}
			}
		}
	})
}

// newSendToWorkspaceHandler returns POST /api/tasks/{id}/send.
// It decodes a JSON body {"text":"..."} and forwards the text to the cmux workspace.
func newSendToWorkspaceHandler(streamer WorkspaceStreamer, provider LiveStreamProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := parseIDFromRequest(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid task id")
			return
		}

		workspaceID, err := provider.WorkspaceIDForTask(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "workspace not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Limit body size before decoding to prevent DoS via large payloads.
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		var req sendToWorkspaceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeJSONError(w, http.StatusBadRequest, "text is required")
			return
		}

		if err := streamer.Send(r.Context(), workspaceID, req.Text); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "send failed")
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}
