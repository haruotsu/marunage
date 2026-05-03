package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestHub_SubscribePublish pins the basic fan-out: a subscriber sees
// every event the hub publishes after Subscribe and before Unsubscribe.
func TestHub_SubscribePublish(t *testing.T) {
	hub := NewHub()

	sub := hub.Subscribe()
	t.Cleanup(func() { hub.Unsubscribe(sub) })

	hub.Publish(Event{Name: "ping", Data: "hello"})

	select {
	case got := <-sub.C:
		if got.Name != "ping" || got.Data != "hello" {
			t.Fatalf("got %+v; want {Name:ping Data:hello}", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive published event within 1s")
	}
}

// TestHub_UnsubscribeStopsDelivery pins that Unsubscribe is durable:
// once a subscription is closed no later Publish ever lands on it.
// Without this guarantee every disconnected SSE client would leak a
// goroutine + a buffered channel until the process exited.
func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	hub := NewHub()

	sub := hub.Subscribe()
	hub.Unsubscribe(sub)

	// Publishing post-unsubscribe must not panic on send to a closed
	// channel and must not block forever waiting for the gone subscriber.
	done := make(chan struct{})
	go func() {
		hub.Publish(Event{Name: "ping"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked after Unsubscribe; subscriber probably still registered")
	}
}

// TestHub_NoGoroutineLeakAfterUnsubscribe pins the leak-free invariant
// — many client cancels in rapid succession must not grow the
// runtime's goroutine count.  This is the SSE-side counterpart to the
// HTTP handler test below.
func TestHub_NoGoroutineLeakAfterUnsubscribe(t *testing.T) {
	hub := NewHub()
	before := runtime.NumGoroutine()

	for i := 0; i < 50; i++ {
		sub := hub.Subscribe()
		hub.Unsubscribe(sub)
	}
	// Give the runtime a moment to settle scheduled goroutines.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("goroutine count grew %d -> %d after subscribe/unsubscribe loop; expected near zero growth", before, after)
	}
}

// TestSSEHandler_HeartbeatPing pins the wire-level contract: a
// connection must receive `event: ping` within the configured
// heartbeat interval.  The brief calls for 30 s in production but we
// inject a tight 50 ms here so the test stays fast.
func TestSSEHandler_HeartbeatPing(t *testing.T) {
	hub := NewHub()
	handler := NewSSEHandler(hub, SSEOptions{HeartbeatInterval: 50 * time.Millisecond})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q; want text/event-stream", got)
	}

	buf := make([]byte, 256)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "event: ping") {
		t.Fatalf("first SSE chunk = %q; want substring 'event: ping'", got)
	}
}

// TestSSEHandler_ClientCancelLeavesNoLeak proves that a client which
// disconnects mid-stream causes the server-side goroutine to exit.
// The fixture opens, then cancels, the connection ten times and asserts
// the goroutine count is unchanged within a small slack window.
func TestSSEHandler_ClientCancelLeavesNoLeak(t *testing.T) {
	hub := NewHub()
	handler := NewSSEHandler(hub, SSEOptions{HeartbeatInterval: 25 * time.Millisecond})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	before := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			cancel()
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			cancel()
			t.Fatalf("Do: %v", err)
		}
		buf := make([]byte, 64)
		_, _ = resp.Body.Read(buf)
		cancel()
		_ = resp.Body.Close()
	}
	// Allow the server-side handlers to observe ctx cancellation and exit.
	time.Sleep(200 * time.Millisecond)
	runtime.GC()

	after := runtime.NumGoroutine()
	if after > before+5 {
		t.Fatalf("goroutine count grew %d -> %d after 10 SSE connect/cancel cycles", before, after)
	}
}
