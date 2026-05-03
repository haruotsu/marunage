package web

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestServer_RunListensAndShutsDown pins the lifecycle: Run binds the
// configured addr:port, /healthz is reachable while Run is alive, and
// cancelling the context unblocks Run within the documented 5s
// shutdown budget.  Port 0 lets the OS pick a free port so the test is
// safe to run in parallel with anything else on the host.
func TestServer_RunListensAndShutsDown(t *testing.T) {
	srv, err := NewServer(Options{
		TokenSource: testTokenSource,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := listener.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- srv.Serve(ctx, listener) }()

	if err := waitForReady(t, "http://"+addr+"/healthz", 2*time.Second); err != nil {
		t.Fatalf("server never became ready: %v", err)
	}

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("body = %q; want %q", body, "ok")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned unexpected error: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Serve did not return within 6s of context cancel; expected graceful shutdown within 5s")
	}
}

func waitForReady(t *testing.T, url string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("timeout waiting for readiness")
}
