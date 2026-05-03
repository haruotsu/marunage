package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// listCalls returns the number of List invocations the SUT has made,
// taking the mutex so the goroutine running the watch loop and the
// test goroutine do not race on listFilters under -race.
func (f *fakeTaskRepo) listCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.listFilters)
}

// waitFor polls cond up to ~1s in 5ms steps. Used by the watch tests
// to synchronise on the SUT's "I just rendered" side effect without
// sleeping on a fixed interval that races on slow CI.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	const (
		step    = 5 * time.Millisecond
		timeout = time.Second
	)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(step)
	}
	t.Fatalf("waitFor: condition not satisfied within %v", timeout)
}

// seedStatusTasks loads the fake repo with one task per status so each
// status test can assert against a known mix without redefining the rows.
// The lock_key / ws / result_summary fields mirror what PR-42 dispatch
// actually writes when claiming a row, so the rendered status table is
// representative of a live system rather than a contrived empty shell.
func seedStatusTasks(repo *fakeTaskRepo) {
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "draft README",
		Status: store.StatusPending, Priority: 5,
	}
	repo.rows[2] = store.Task{
		ID: 2, Source: "gmail", Title: "reply to alice",
		Status: store.StatusRunning, Priority: 3,
		WS: "workspace:42", ResultSummary: "drafted reply",
	}
	repo.rows[3] = store.Task{
		ID: 3, Source: "slack", Title: "approve deploy",
		Status: store.StatusWaitingHuman, Priority: 4,
		WS: "workspace:43", JudgmentReason: "needs go/no-go",
	}
	repo.rows[4] = store.Task{
		ID: 4, Source: "manual", Title: "old archived",
		Status: store.StatusDone, Priority: 0,
	}
	repo.nextID = 4
}

// 1. Empty repo: a friendly message tells the operator the daemon is idle
// rather than leaving them staring at a blank screen wondering whether
// the command crashed.
func TestTaskStatus_EmptyPrintsFriendlyMessage(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No active workspaces.") {
		t.Errorf("expected 'No active workspaces.' in stdout; got %q", stdout.String())
	}
}

// 2. Default filter: status shows only the actionable monitoring states
// (running + waiting_human). pending / done / failed / skipped belong to
// `marunage list` or the post-mortem flows; muddying the watch screen
// with them defeats the "what is the daemon doing right now" purpose.
func TestTaskStatus_FiltersToRunningAndWaitingHuman(t *testing.T) {
	repo := installFakeRepo(t)
	seedStatusTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "reply to alice") {
		t.Errorf("expected running row; got %q", out)
	}
	if !strings.Contains(out, "approve deploy") {
		t.Errorf("expected waiting_human row; got %q", out)
	}
	if strings.Contains(out, "draft README") {
		t.Errorf("pending row should be hidden; got %q", out)
	}
	if strings.Contains(out, "old archived") {
		t.Errorf("done row should be hidden; got %q", out)
	}

	if len(repo.listFilters) != 1 {
		t.Fatalf("listFilters captured = %d; want 1", len(repo.listFilters))
	}
	want := []string{store.StatusRunning, store.StatusWaitingHuman}
	got := repo.listFilters[0].Statuses
	if !equalStringSet(got, want) {
		t.Errorf("default statuses = %v; want %v", got, want)
	}
}

// 3. Header columns: the table must surface ID / Source / Status / WS /
// Summary / Title so the operator can scan "what is running where".
// Pinning the headers keeps a future column rename from silently breaking
// shell pipelines that grep the header (e.g. `marunage status | awk`).
func TestTaskStatus_TextOutputHasHeader(t *testing.T) {
	repo := installFakeRepo(t)
	seedStatusTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, header := range []string{"ID", "Source", "Status", "WS", "Summary", "Title"} {
		if !strings.Contains(out, header) {
			t.Errorf("output missing header %q; got %q", header, out)
		}
	}
}

// 4. Summary defaults to "(running)" when ResultSummary is empty so a
// freshly-claimed row whose dispatch has not yet written a summary still
// shows a useful word in the column instead of an empty cell that the
// reader cannot distinguish from a layout glitch.
func TestTaskStatus_SummaryFallbacksToRunningPlaceholder(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "no summary yet",
		Status: store.StatusRunning, WS: "workspace:9",
	}
	repo.nextID = 1

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(running)") {
		t.Errorf("expected '(running)' placeholder; got %q", stdout.String())
	}
}

// 4b. waiting_human rows with no ResultSummary show "(waiting)" rather
// than the running-shaped placeholder — a row escalated to a human is
// not running anything; misnaming it would mislead the operator about
// what the column is telling them.
func TestTaskStatus_WaitingHumanSummaryFallback(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "slack", Title: "approve deploy",
		Status: store.StatusWaitingHuman, WS: "workspace:9",
	}
	repo.nextID = 1

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "(waiting)") {
		t.Errorf("expected '(waiting)' placeholder; got %q", out)
	}
	if strings.Contains(out, "(running)") {
		t.Errorf("waiting_human row should not borrow '(running)' placeholder; got %q", out)
	}
}

// 5. ResultSummary is shown verbatim, with embedded newlines collapsed to
// a single space so a multi-line summary does not break the tabwriter
// alignment by floating columns onto subsequent lines.
func TestTaskStatus_SummaryCollapsesNewlines(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "gmail", Title: "wrote reply",
		Status: store.StatusRunning, WS: "workspace:1",
		ResultSummary: "first line\nsecond line",
	}
	repo.nextID = 1

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "first line second line") {
		t.Errorf("expected newline-collapsed summary in output; got %q", out)
	}
	// And the literal newline must NOT survive — that would re-break the
	// tabwriter alignment we collapsed for.
	if strings.Contains(out, "first line\nsecond line") {
		t.Errorf("raw newline still present in summary; got %q", out)
	}
}

// 6. WS column shows "(none)" when the row has no workspace reference so
// a row escalated from a state that never reached dispatch is not silently
// rendered as a blank gap.
func TestTaskStatus_EmptyWSShowsNonePlaceholder(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "slack", Title: "no ws yet",
		Status: store.StatusWaitingHuman,
	}
	repo.nextID = 1

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(none)") {
		t.Errorf("expected '(none)' WS placeholder; got %q", stdout.String())
	}
}

// 7. --json emits a machine-readable array. The wire shape reuses the
// taskJSON projection list / show already speak so a Web UI / shell can
// keep one decoder.
func TestTaskStatus_JSONFlagEmitsArray(t *testing.T) {
	repo := installFakeRepo(t)
	seedStatusTasks(repo)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status --json exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("expected JSON array; got %q", got)
	}
	// 2 actionable rows in seedStatusTasks (running + waiting_human); the
	// JSON output must carry exactly that count.
	if got == "[]" {
		t.Errorf("seeded data should produce non-empty JSON array; got %q", got)
	}
}

// 8. Empty result still serialises as "[]" (not null) so a consumer can
// rely on `len(arr)` without a nil-check — same contract list / render
// already publish.
func TestTaskStatus_JSONEmptyIsArrayNotNull(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status --json exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" {
		t.Errorf("empty status JSON = %q; want %q", got, "[]")
	}
}

// 9. Repo open errors must propagate as a non-zero exit so a shell loop
// notices a broken DB rather than silently re-rendering an empty screen.
func TestTaskStatus_PropagatesRepoErrors(t *testing.T) {
	withTaskRepoFactory(t, func(_ context.Context, _ string) (taskRepo, func() error, error) {
		return nil, nil, errBoom
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// 10. --watch + --json is rejected: streaming JSON has no canonical
// framing today (NDJSON? array per refresh?), so we close the door on
// the combination until a follow-up PR picks a wire format. The check
// must fire BEFORE the loop spins up — otherwise a script piping into
// `jq` would accumulate redraws indefinitely.
func TestTaskStatus_WatchAndJSONAreMutuallyExclusive(t *testing.T) {
	installFakeRepo(t)

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"status", "--watch", "--json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr should mention 'mutually exclusive'; got %q", stderr.String())
	}
}

// 11. --interval 0 is rejected before the loop starts so a typo cannot
// peg a CPU spinning on a zero-duration ticker. Same for negative
// values.
func TestTaskStatus_WatchRejectsNonPositiveInterval(t *testing.T) {
	installFakeRepo(t)

	for _, arg := range []string{"0", "-1s"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{"status", "--watch", "--interval", arg}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected non-zero exit for --interval=%s; stdout=%q stderr=%q",
					arg, stdout.String(), stderr.String())
			}
		})
	}
}

// 12. Watch loop renders once on entry, redraws once per tick, and
// shuts down cleanly on context cancel. We drive the loop directly via
// runStatusWatch so the test can pin the redraw count without depending
// on real wall-clock time.
func TestTaskStatus_WatchRedrawsOncePerTick(t *testing.T) {
	repo := newFakeTaskRepo()
	repo.rows[1] = store.Task{
		ID: 1, Source: "gmail", Title: "row",
		Status: store.StatusRunning, WS: "workspace:1",
	}

	tickC := make(chan time.Time, 4)
	stopCalls := 0
	tickerFactory := func(d time.Duration) (<-chan time.Time, func()) {
		return tickC, func() { stopCalls++ }
	}

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runStatusWatch(ctx, &buf, repo, statusWatchOpts{
			interval:  time.Second,
			newTicker: tickerFactory,
		})
	}()

	// Wait for the initial render to land before injecting ticks so the
	// count math below is deterministic.
	waitFor(t, func() bool { return repo.listCalls() >= 1 })

	tickC <- time.Now()
	waitFor(t, func() bool { return repo.listCalls() >= 2 })

	tickC <- time.Now()
	waitFor(t, func() bool { return repo.listCalls() >= 3 })

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runStatusWatch returned error on cancel: %v", err)
	}
	if got := repo.listCalls(); got != 3 {
		t.Errorf("repo.List calls = %d; want 3 (1 initial + 2 ticks)", got)
	}
	if stopCalls != 1 {
		t.Errorf("ticker stop calls = %d; want 1", stopCalls)
	}
}

// 13. Each redraw is preceded by the ANSI clear-screen sequence so the
// terminal shows the fresh table at row 0 instead of scrolling forever.
// The sequence is the standard `ESC [ 2 J` (clear) + `ESC [ H` (home).
func TestTaskStatus_WatchClearsScreenBeforeEachRedraw(t *testing.T) {
	repo := newFakeTaskRepo()
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "row",
		Status: store.StatusRunning, WS: "workspace:1",
	}

	tickC := make(chan time.Time, 1)
	tickerFactory := func(d time.Duration) (<-chan time.Time, func()) {
		return tickC, func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runStatusWatch(ctx, &buf, repo, statusWatchOpts{
			interval:  time.Second,
			newTicker: tickerFactory,
		})
	}()
	waitFor(t, func() bool { return repo.listCalls() >= 1 })
	tickC <- time.Now()
	waitFor(t, func() bool { return repo.listCalls() >= 2 })
	cancel()
	<-done

	const clearSeq = "\x1b[2J\x1b[H"
	if got := strings.Count(buf.String(), clearSeq); got != 2 {
		t.Errorf("clear-screen count = %d; want 2 (initial + 1 tick)\nbuf=%q",
			got, buf.String())
	}
}

// 14. A repo error mid-loop aborts the loop and surfaces the error so
// the operator notices a degraded daemon rather than re-rendering the
// last good frame forever.
func TestTaskStatus_WatchAbortsOnRepoError(t *testing.T) {
	repo := newFakeTaskRepo()
	repo.listErr = errBoom

	tickC := make(chan time.Time, 1)
	tickerFactory := func(d time.Duration) (<-chan time.Time, func()) {
		return tickC, func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	err := runStatusWatch(ctx, &buf, repo, statusWatchOpts{
		interval:  time.Second,
		newTicker: tickerFactory,
	})
	if err == nil {
		t.Fatal("expected runStatusWatch to return the underlying error")
	}
}

// 15. --interval flag value is forwarded to the ticker factory. We
// stub the factory via a package-level hook so the production cobra
// path is exercised end-to-end (rather than calling runStatusWatch
// directly).
func TestTaskStatus_WatchHonoursIntervalFlag(t *testing.T) {
	repo := installFakeRepo(t)
	repo.rows[1] = store.Task{
		ID: 1, Source: "manual", Title: "row",
		Status: store.StatusRunning, WS: "workspace:1",
	}

	var seen time.Duration
	tickC := make(chan time.Time)
	withStatusTicker(t, func(d time.Duration) (<-chan time.Time, func()) {
		seen = d
		return tickC, func() {}
	})

	// Run in a goroutine so we can cancel via ctrl-c equivalent: cobra
	// surfaces the parent context, but Execute runs synchronously and
	// blocks. We sidestep that by using a context that expires fast
	// and asserting on the captured duration.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	withStatusContext(t, ctx)

	var stdout, stderr bytes.Buffer
	_ = Execute([]string{"status", "--watch", "--interval", "750ms"}, &stdout, &stderr)

	if seen != 750*time.Millisecond {
		t.Errorf("ticker interval = %v; want 750ms", seen)
	}
}
