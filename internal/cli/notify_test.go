package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/store"
)

// captureNotifier records the messages the notify command delivers.
type captureNotifier struct{ msgs []string }

func (c *captureNotifier) Notify(_ context.Context, m string) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func withNotifier(t *testing.T, n Notifier) {
	t.Helper()
	prev := notifierHook
	notifierHook = n
	t.Cleanup(func() { notifierHook = prev })
}

// NT1: with the default config (on_complete + on_failure both true), notify
// surfaces done, failed and waiting_human rows — but not pending or running.
func TestNotify_SendsForTerminalAndWaitingStates(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "done one", Status: store.StatusDone}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "failed one", Status: store.StatusFailed}
	repo.rows[3] = store.Task{ID: 3, Source: "manual", Title: "needs human", Status: store.StatusWaitingHuman}
	repo.rows[4] = store.Task{ID: 4, Source: "manual", Title: "still pending", Status: store.StatusPending}
	repo.rows[5] = store.Task{ID: 5, Source: "manual", Title: "running now", Status: store.StatusRunning}

	cap := &captureNotifier{}
	withNotifier(t, cap)

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"notify"}, missingConfig(t)...), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("notify exit=%d; stderr=%q", code, stderr.String())
	}
	if len(cap.msgs) != 3 {
		t.Fatalf("notified %d task(s); want 3\nmsgs=%v", len(cap.msgs), cap.msgs)
	}
	joined := strings.Join(cap.msgs, "\n")
	for _, want := range []string{"done one", "failed one", "needs human"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notifications missing %q\n%s", want, joined)
		}
	}
	for _, no := range []string{"still pending", "running now"} {
		if strings.Contains(joined, no) {
			t.Errorf("notifications unexpectedly include %q\n%s", no, joined)
		}
	}
	if !strings.Contains(stdout.String(), "Sent 3 notification(s).") {
		t.Errorf("stdout=%q; want 'Sent 3 notification(s).'", stdout.String())
	}
}

// NT2: with no notifier injected, the default delivery prints each
// notification to stdout so the command is useful out of the box.
func TestNotify_DefaultNotifierPrintsToStdout(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "review me", Status: store.StatusWaitingHuman}
	withNotifier(t, nil)

	var stdout, stderr bytes.Buffer
	code := Execute(append([]string{"notify"}, missingConfig(t)...), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("notify exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "review me") {
		t.Errorf("stdout=%q; want it to include the task title", stdout.String())
	}
	if !strings.Contains(stdout.String(), "[waiting_human] #1") {
		t.Errorf("stdout=%q; want a '[waiting_human] #1' prefix", stdout.String())
	}
}

// NT3: [notify].on_complete=false + on_failure=false suppresses done/failed,
// but waiting_human is always surfaced because it needs a human.
func TestNotify_RespectsConfigToggles(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{ID: 1, Source: "manual", Title: "done one", Status: store.StatusDone}
	repo.rows[2] = store.Task{ID: 2, Source: "manual", Title: "failed one", Status: store.StatusFailed}
	repo.rows[3] = store.Task{ID: 3, Source: "manual", Title: "needs human", Status: store.StatusWaitingHuman}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[notify]\non_complete = false\non_failure = false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cap := &captureNotifier{}
	withNotifier(t, cap)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"notify", "--config", cfgPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("notify exit=%d; stderr=%q", code, stderr.String())
	}
	if len(cap.msgs) != 1 {
		t.Fatalf("notified %d task(s); want 1 (waiting_human only)\nmsgs=%v", len(cap.msgs), cap.msgs)
	}
	if !strings.Contains(cap.msgs[0], "needs human") {
		t.Errorf("notification = %q; want the waiting_human row", cap.msgs[0])
	}
}
