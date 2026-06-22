package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/store"
)

// drainingDispatcher marks up to MaxParallel pending rows done on each Run,
// emulating a real dispatcher shrinking the queue so run-all's drain loop can
// be observed end-to-end without cmux/sqlite.
type drainingDispatcher struct {
	repo  *fakeTaskRepo
	calls int
}

func (d *drainingDispatcher) Run(_ context.Context, opts dispatch.RunOptions) error {
	d.calls++
	d.repo.mu.Lock()
	defer d.repo.mu.Unlock()
	n := 0
	for id, row := range d.repo.rows {
		if n >= opts.MaxParallel {
			break
		}
		if row.Status == store.StatusPending {
			row.Status = store.StatusDone
			d.repo.rows[id] = row
			n++
		}
	}
	return nil
}

// missingConfig returns a --config path that does not exist so config.Load
// falls back to Default() (max_parallel=3) deterministically, regardless of
// whether the developer running the test has a real ~/.marunage/config.toml.
func missingConfig(t *testing.T) []string {
	t.Helper()
	return []string{"--config", filepath.Join(t.TempDir(), "config.toml")}
}

// RA1: run-all keeps dispatching until no pending row remains, even when one
// dispatcher pass (capped at max_parallel) cannot drain the whole queue.
func TestRunAll_DrainsEntirePendingQueue(t *testing.T) {
	repo := installFakeRepo(t)
	for i := int64(1); i <= 5; i++ {
		repo.rows[i] = store.Task{ID: i, Source: "manual", Title: "t", Status: store.StatusPending}
	}
	d := &drainingDispatcher{repo: repo}
	withDispatcherFactory(t, func(_ context.Context, _ string) (dispatchRunner, func() error, error) {
		return d, func() error { return nil }, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"run-all"}, missingConfig(t)...), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run-all exit=%d; stderr=%q", code, stderr.String())
	}
	for _, row := range repo.rows {
		if row.Status == store.StatusPending {
			t.Errorf("pending row survived run-all: %+v", row)
		}
	}
	if d.calls < 2 {
		t.Errorf("dispatcher Run called %d times; want >=2 (5 rows / max_parallel 3)", d.calls)
	}
	if !strings.Contains(stdout.String(), "Dispatched 5 task(s).") {
		t.Errorf("stdout=%q; want 'Dispatched 5 task(s).'", stdout.String())
	}
}

// RA2: an empty queue dispatches nothing and never opens a dispatcher pass.
func TestRunAll_EmptyQueueDispatchesNothing(t *testing.T) {
	repo := installFakeRepo(t)
	d := &drainingDispatcher{repo: repo}
	withDispatcherFactory(t, func(_ context.Context, _ string) (dispatchRunner, func() error, error) {
		return d, func() error { return nil }, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"run-all"}, missingConfig(t)...), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run-all exit=%d; stderr=%q", code, stderr.String())
	}
	if d.calls != 0 {
		t.Errorf("dispatcher Run called %d times; want 0 on empty queue", d.calls)
	}
	if !strings.Contains(stdout.String(), "Dispatched 0 task(s).") {
		t.Errorf("stdout=%q; want 'Dispatched 0 task(s).'", stdout.String())
	}
}

// RA3: a dispatcher error propagates as a non-zero exit.
func TestRunAll_RunErrorPropagates(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "t", Status: store.StatusPending}
	fd := &fakeDispatcher{runErr: errors.New("boom in dispatcher")}
	withDispatcherFactory(t, func(_ context.Context, _ string) (dispatchRunner, func() error, error) {
		return fd, func() error { return nil }, nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"run-all"}, missingConfig(t)...), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("run-all exit=0; want non-zero on dispatcher error; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom in dispatcher") {
		t.Errorf("stderr=%q; want underlying error mentioned", stderr.String())
	}
}

// RA4: when a pass makes no progress (every survivor blocked), run-all stops
// after a single no-progress pass rather than spinning forever.
func TestRunAll_StopsWhenNoProgress(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "stuck", Status: store.StatusPending}
	fd := installFakeDispatcher(t) // no-op Run, never drains the queue

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"run-all"}, missingConfig(t)...), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run-all exit=%d; stderr=%q", code, stderr.String())
	}
	if len(fd.runCalls) != 1 {
		t.Errorf("dispatcher Run called %d times; want exactly 1 (stall guard)", len(fd.runCalls))
	}
	if !strings.Contains(stdout.String(), "Dispatched 0 task(s).") {
		t.Errorf("stdout=%q; want 'Dispatched 0 task(s).'", stdout.String())
	}
}
