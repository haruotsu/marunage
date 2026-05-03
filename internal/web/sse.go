package web

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one Server-Sent Events frame on the wire.  Name maps to the
// `event:` line and Data to the `data:` line. The hub is content-agnostic
// so PR-91 can wire real task / discovery events without touching this
// package.
type Event struct {
	Name string
	Data string
}

// subscriberBuffer caps how many events one slow subscriber can have
// queued before the hub starts dropping for that subscription. Small
// enough to bound memory; large enough to absorb a normal HTTP write
// stall without dropping every burst.
const subscriberBuffer = 16

// defaultMaxSubscribers caps the total number of in-flight SSE
// subscribers a single Hub will admit.  --remote mode currently has
// no auth, so an unbounded subscriber count would let any reachable
// client spawn a goroutine + 16-event buffer per connection until the
// process exhausts file descriptors.  64 is generous for a
// single-operator marunage instance and conservative for a
// shared-network deployment.
const defaultMaxSubscribers = 64

// Subscription is the handle returned by Hub.Subscribe.  Consumers
// read from C; the hub owns the channel's lifetime so callers must
// hand it back via Hub.Unsubscribe rather than closing C themselves.
type Subscription struct {
	C chan Event
}

// Hub is the in-process pub/sub the SSE handler hangs off.  PR-62 only
// produces heartbeat pings via the SSE handler; PR-91 will start
// publishing dispatch / discovery events through the same surface.
type Hub struct {
	mu      sync.Mutex
	subs    map[*Subscription]struct{}
	maxSubs int
}

// NewHub returns an empty hub ready to accept subscribers, capped at
// defaultMaxSubscribers.
func NewHub() *Hub {
	return NewHubWithCap(defaultMaxSubscribers)
}

// NewHubWithCap is the test seam: lets the regression test pin the
// cap without depending on the package-level default.  Production
// callers should use NewHub.
func NewHubWithCap(maxSubs int) *Hub {
	if maxSubs <= 0 {
		maxSubs = defaultMaxSubscribers
	}
	return &Hub{
		subs:    make(map[*Subscription]struct{}),
		maxSubs: maxSubs,
	}
}

// Subscribe registers a new subscriber and returns the handle.  When
// the hub already holds maxSubs connections the call returns nil so
// the SSE handler can refuse the request with 503; the caller MUST
// nil-check the return value.
func (h *Hub) Subscribe() *Subscription {
	h.mu.Lock()
	if len(h.subs) >= h.maxSubs {
		h.mu.Unlock()
		return nil
	}
	sub := &Subscription{C: make(chan Event, subscriberBuffer)}
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Unsubscribe removes sub and closes its channel.  Safe to call more
// than once: the second call is a no-op rather than a panic on a
// double close.
func (h *Hub) Unsubscribe(sub *Subscription) {
	h.mu.Lock()
	if _, ok := h.subs[sub]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.subs, sub)
	h.mu.Unlock()
	close(sub.C)
}

// Publish fans event out to every active subscriber.  Slow consumers
// (a full buffer) are dropped for this event rather than backpressuring
// the publisher — losing one ping is preferable to stalling dispatch
// because a browser tab is paused.
func (h *Hub) Publish(event Event) {
	h.mu.Lock()
	targets := make([]*Subscription, 0, len(h.subs))
	for sub := range h.subs {
		targets = append(targets, sub)
	}
	h.mu.Unlock()

	for _, sub := range targets {
		select {
		case sub.C <- event:
		default:
			// drop for this subscriber; logging is intentionally
			// deferred to PR-91 when real events flow through the hub.
		}
	}
}

// SSEOptions tunes the SSE handler.  Tests inject a tight heartbeat
// interval; production uses 30s per the brief.
type SSEOptions struct {
	HeartbeatInterval time.Duration
}

const defaultHeartbeat = 30 * time.Second

// NewSSEHandler returns an http.Handler that streams hub events to the
// client as text/event-stream and emits a periodic `event: ping`
// heartbeat so intermediaries don't drop the idle connection.
func NewSSEHandler(hub *Hub, opts SSEOptions) http.Handler {
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = defaultHeartbeat
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

		sub := hub.Subscribe()
		if sub == nil {
			// Hub at capacity: drop the connection cleanly with 503
			// + Retry-After so well-behaved clients back off.
			w.Header().Set("Retry-After", "30")
			http.Error(w, "sse: subscriber capacity reached", http.StatusServiceUnavailable)
			return
		}
		defer hub.Unsubscribe(sub)

		// Send an immediate ping so the test (and a real client) can
		// confirm the channel is live without waiting a full interval.
		writePing(w, flusher)

		ticker := time.NewTicker(opts.HeartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				writePing(w, flusher)
			case event, ok := <-sub.C:
				if !ok {
					return
				}
				writeEvent(w, flusher, event)
			}
		}
	})
}

func writePing(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprintf(w, "event: ping\ndata: %d\n\n", time.Now().UnixMilli())
	flusher.Flush()
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, ev Event) {
	if ev.Name != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Name)
	}
	fmt.Fprintf(w, "data: %s\n\n", ev.Data)
	flusher.Flush()
}
