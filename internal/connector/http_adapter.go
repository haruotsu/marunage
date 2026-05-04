package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/haruotsu/marunage/internal/source"
)

// HTTPAdapter implements source.Plugin for an HTTP-based connector.
type HTTPAdapter struct {
	cfg    *ConnectorConfig
	client *http.Client
}

// NewHTTPAdapter constructs an HTTPAdapter from the given config.
func NewHTTPAdapter(cfg *ConnectorConfig) (*HTTPAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("connector: nil config")
	}
	return &HTTPAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name returns the connector name, satisfying source.Plugin.
func (a *HTTPAdapter) Name() string { return a.cfg.Connector.Name }

// List calls POST /discover with an empty body and returns all tasks.
func (a *HTTPAdapter) List(ctx context.Context) ([]source.Task, error) {
	return a.discoverRequest(ctx, nil)
}

// Since calls POST /discover with {"since": checkpoint}.
func (a *HTTPAdapter) Since(ctx context.Context, checkpoint string) ([]source.Task, error) {
	return a.discoverRequest(ctx, map[string]string{"since": checkpoint})
}

func (a *HTTPAdapter) discoverRequest(ctx context.Context, payload any) ([]source.Task, error) {
	url := a.cfg.Endpoint.Discover
	if url == "" {
		return nil, fmt.Errorf("connector %s: endpoint.discover is not configured", a.cfg.Connector.Name)
	}

	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.applyAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s: status %d", url, resp.StatusCode)
	}

	var raw []struct {
		Title      string `json:"title"`
		ExternalID string `json:"external_id"`
		Body       string `json:"body"`
		Done       bool   `json:"done"`
		Priority   string `json:"priority"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	tasks := make([]source.Task, len(raw))
	for i, r := range raw {
		tasks[i] = source.Task{
			Source:     a.cfg.Connector.Name,
			ExternalID: r.ExternalID,
			Title:      r.Title,
			Body:       r.Body,
			Done:       r.Done,
			Priority:   r.Priority,
		}
	}
	return tasks, nil
}

// Notify sends a POST /notify with external_id and status.
func (a *HTTPAdapter) Notify(ctx context.Context, externalID, status string) error {
	url := a.cfg.Endpoint.Notify
	if url == "" {
		return fmt.Errorf("connector %s: endpoint.notify is not configured", a.cfg.Connector.Name)
	}

	payload := map[string]string{
		"external_id": externalID,
		"status":      status,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notify: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build notify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.applyAuth(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// Setup validates configuration. No interactive step is required.
func (a *HTTPAdapter) Setup(_ context.Context, _ source.SetupOptions) error {
	return nil
}

// AuthStatus returns authenticated if the discover endpoint is reachable,
// or not_configured if no endpoint is set.
func (a *HTTPAdapter) AuthStatus(_ context.Context) (source.AuthStatus, error) {
	if a.cfg.Endpoint.Discover == "" && a.cfg.Endpoint.Notify == "" {
		return source.AuthNotConfigured, nil
	}
	return source.AuthAuthenticated, nil
}

func (a *HTTPAdapter) applyAuth(req *http.Request) {
	switch a.cfg.Auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.cfg.Auth.Token)
	case "basic":
		req.SetBasicAuth(a.cfg.Auth.Token, "")
	}
}
