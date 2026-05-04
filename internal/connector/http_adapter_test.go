package connector_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haruotsu/marunage/internal/connector"
	"github.com/haruotsu/marunage/internal/source"
)

func newTestAdapter(t *testing.T, cfg *connector.ConnectorConfig) *connector.HTTPAdapter {
	t.Helper()
	a, err := connector.NewHTTPAdapter(cfg)
	if err != nil {
		t.Fatalf("NewHTTPAdapter: %v", err)
	}
	return a
}

func discoverServer(t *testing.T, tasks []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tasks)
	}))
}

func TestHTTPAdapter_Name(t *testing.T) {
	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "test-hook", Type: "discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)
	if a.Name() != "test-hook" {
		t.Errorf("Name: got %q, want %q", a.Name(), "test-hook")
	}
}

func TestHTTPAdapter_List(t *testing.T) {
	srv := discoverServer(t, []map[string]any{
		{"title": "Task A", "external_id": "id-1"},
		{"title": "Task B", "external_id": "id-2", "body": "details"},
	})
	defer srv.Close()

	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Endpoint:  connector.EndpointSection{Discover: srv.URL + "/discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)

	tasks, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks count: got %d, want 2", len(tasks))
	}
	if tasks[0].Title != "Task A" {
		t.Errorf("tasks[0].Title: got %q", tasks[0].Title)
	}
	if tasks[0].ExternalID != "id-1" {
		t.Errorf("tasks[0].ExternalID: got %q", tasks[0].ExternalID)
	}
	if tasks[1].Body != "details" {
		t.Errorf("tasks[1].Body: got %q", tasks[1].Body)
	}
	if tasks[0].Source != "webhook" {
		t.Errorf("tasks[0].Source: got %q, want %q", tasks[0].Source, "webhook")
	}
}

func TestHTTPAdapter_Since(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Endpoint:  connector.EndpointSection{Discover: srv.URL + "/discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)

	_, err := a.Since(context.Background(), "checkpoint-123")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}

	var body map[string]string
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if body["since"] != "checkpoint-123" {
		t.Errorf("request body since: got %q, want %q", body["since"], "checkpoint-123")
	}
}

func TestHTTPAdapter_List_BearerAuth(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Endpoint:  connector.EndpointSection{Discover: srv.URL + "/discover"},
		Auth:      connector.AuthSection{Type: "bearer", Token: "my-secret-token"},
	}
	a := newTestAdapter(t, cfg)

	if _, err := a.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if capturedAuth != "Bearer my-secret-token" {
		t.Errorf("Authorization header: got %q, want %q", capturedAuth, "Bearer my-secret-token")
	}
}

func TestHTTPAdapter_Notify(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "notify"},
		Endpoint:  connector.EndpointSection{Notify: srv.URL + "/notify"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)

	err := a.Notify(context.Background(), "task-ext-1", "done")
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var body map[string]string
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["external_id"] != "task-ext-1" {
		t.Errorf("external_id: got %q", body["external_id"])
	}
	if body["status"] != "done" {
		t.Errorf("status: got %q", body["status"])
	}
}

func TestHTTPAdapter_AuthStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Endpoint:  connector.EndpointSection{Discover: srv.URL + "/discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)

	status, err := a.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if status != source.AuthAuthenticated {
		t.Errorf("AuthStatus: got %q, want %q", status, source.AuthAuthenticated)
	}
}

func TestHTTPAdapter_Setup(t *testing.T) {
	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)
	if err := a.Setup(context.Background(), source.SetupOptions{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

func TestHTTPAdapter_ImplementsPlugin(t *testing.T) {
	cfg := &connector.ConnectorConfig{
		Connector: connector.ConnectorSection{Name: "webhook", Type: "discover"},
		Auth:      connector.AuthSection{Type: "none"},
	}
	a := newTestAdapter(t, cfg)
	var _ source.Plugin = a
}
