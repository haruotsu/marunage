package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// fakeWorkspaceStreamer is a test double for WorkspaceStreamer.
type fakeWorkspaceStreamer struct {
	mu      sync.Mutex
	output  string
	outErr  error
	sendErr error
	sent    []string
}

func (f *fakeWorkspaceStreamer) ReadOutput(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, f.outErr
}

func (f *fakeWorkspaceStreamer) Send(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return f.sendErr
}

func (f *fakeWorkspaceStreamer) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

// fakeLiveStreamProvider is a test double for LiveStreamProvider.
type fakeLiveStreamProvider struct {
	workspaceID string
	err         error
}

func (f *fakeLiveStreamProvider) WorkspaceIDForTask(_ context.Context, _ int64) (string, error) {
	return f.workspaceID, f.err
}

// doStreamRequest issues a GET to the live stream handler with the id path value set.
func doStreamRequest(h http.Handler, taskID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID+"/stream", nil)
	req.SetPathValue("id", taskID)
	rec := httptest.NewRecorder()
	// Use a cancelable context so the SSE loop exits.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so handler exits after sending initial ping
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// TestLiveStreamHandler_BadID: non-numeric ID -> 400.
func TestLiveStreamHandler_BadID(t *testing.T) {
	h := newLiveStreamHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{workspaceID: "workspace:1"},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/notanumber/stream", nil)
	req.SetPathValue("id", "notanumber")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestLiveStreamHandler_WorkspaceNotFound: provider returns ErrNotFound -> 404.
func TestLiveStreamHandler_WorkspaceNotFound(t *testing.T) {
	h := newLiveStreamHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{err: store.ErrNotFound},
	)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/1/stream", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestLiveStreamHandler_SSEHeaders: valid workspace -> SSE Content-Type + initial ping.
func TestLiveStreamHandler_SSEHeaders(t *testing.T) {
	streamer := &fakeWorkspaceStreamer{output: ""}
	provider := &fakeLiveStreamProvider{workspaceID: "workspace:1"}

	mux := http.NewServeMux()
	mux.Handle("GET /api/tasks/{id}/stream", newLiveStreamHandler(streamer, provider))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/tasks/1/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context") {
		t.Fatalf("Do: %v", err)
	}
	if resp == nil {
		return
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q; want text/event-stream", got)
	}

	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "event: ping") {
		t.Fatalf("first SSE chunk = %q; want substring 'event: ping'", got)
	}
}

// doSendRequest issues a POST to the send handler with the id path value set.
func doSendRequest(h http.Handler, taskID string, body []byte) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/send", nil)
	}
	req.SetPathValue("id", taskID)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSendToWorkspaceHandler_BadID: non-numeric ID -> 400.
func TestSendToWorkspaceHandler_BadID(t *testing.T) {
	h := newSendToWorkspaceHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{workspaceID: "workspace:1"},
	)
	body, _ := json.Marshal(map[string]string{"text": "hello"})
	rec := doSendRequest(h, "notanumber", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestSendToWorkspaceHandler_WorkspaceNotFound: provider returns ErrNotFound -> 404.
func TestSendToWorkspaceHandler_WorkspaceNotFound(t *testing.T) {
	h := newSendToWorkspaceHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{err: store.ErrNotFound},
	)
	body, _ := json.Marshal(map[string]string{"text": "hello"})
	rec := doSendRequest(h, "1", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404", rec.Code)
	}
}

// TestSendToWorkspaceHandler_InvalidJSON: malformed body -> 400.
func TestSendToWorkspaceHandler_InvalidJSON(t *testing.T) {
	h := newSendToWorkspaceHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{workspaceID: "workspace:1"},
	)
	rec := doSendRequest(h, "1", []byte("not-json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestSendToWorkspaceHandler_EmptyText: empty text -> 400.
func TestSendToWorkspaceHandler_EmptyText(t *testing.T) {
	h := newSendToWorkspaceHandler(
		&fakeWorkspaceStreamer{},
		&fakeLiveStreamProvider{workspaceID: "workspace:1"},
	)
	body, _ := json.Marshal(map[string]string{"text": "   "})
	rec := doSendRequest(h, "1", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
}

// TestSendToWorkspaceHandler_Success: valid request -> 200 + text forwarded to streamer.
func TestSendToWorkspaceHandler_Success(t *testing.T) {
	streamer := &fakeWorkspaceStreamer{}
	h := newSendToWorkspaceHandler(
		streamer,
		&fakeLiveStreamProvider{workspaceID: "workspace:42"},
	)
	body, _ := json.Marshal(map[string]string{"text": "hello workspace"})
	rec := doSendRequest(h, "1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	sent := streamer.Sent()
	if len(sent) != 1 || sent[0] != "hello workspace" {
		t.Errorf("streamer.Sent() = %v; want [\"hello workspace\"]", sent)
	}
}

// TestSendToWorkspaceHandler_SendError: streamer returns error -> 500.
func TestSendToWorkspaceHandler_SendError(t *testing.T) {
	h := newSendToWorkspaceHandler(
		&fakeWorkspaceStreamer{sendErr: errors.New("cmux send failed")},
		&fakeLiveStreamProvider{workspaceID: "workspace:1"},
	)
	body, _ := json.Marshal(map[string]string{"text": "hello"})
	rec := doSendRequest(h, "1", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500", rec.Code)
	}
}
