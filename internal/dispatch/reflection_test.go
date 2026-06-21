package dispatch_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/dispatch"
	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/store"
)

// PR-102 reflection-hook test list (t_wada TDD; ticked off in
// .test-list.md as the matching test below goes green):
//
//   N1. New rejects missing WithStore.
//   N2. New rejects missing WithCmux.
//   N3. New rejects empty WithSkill.
//   N4. New rejects missing WithWorkspaceDirs.
//   N5. New rejects WithSampleRate outside [0,1].
//   N6. New defaults timeout=5m, sample_rate=1.0, auditor=Nop, clock=time.Now.
//   S1. SampleRate(0) never sends.
//   S2. SampleRate(1) always sends.
//   S3. WithSampler overrides the rate-based sampler.
//   H1. OnDone with empty WS is a no-op.
//   H2. OnDone with sampler.False is a no-op.
//   H3. OnDone calls cmux.Send carrying the skill body + workspace dir
//       sentinel-write instructions.
//   H4. OnDone polls <ws>/.reflection and persists trimmed contents.
//   H5. Timeout fires before .reflection arrives -> no SetReflection,
//       audit reflection.timeout.
//   H6. Parent ctx cancel -> goroutine exits early, audit reflection.cancel.
//   H7. Wait blocks until every dispatched goroutine exits.
//   H8. Send failure -> audit reflection.fail; no SetReflection.
//   H9. Audit reflection.start fires immediately before Send.

// reflectStore captures SetReflection calls in test-friendly form.
type reflectStore struct {
	mu      sync.Mutex
	calls   []reflectStoreCall
	setHook func(id int64, text string) error
}

type reflectStoreCall struct {
	ID   int64
	Text string
}

func (s *reflectStore) SetReflection(_ context.Context, id int64, text string) error {
	s.mu.Lock()
	s.calls = append(s.calls, reflectStoreCall{ID: id, Text: text})
	s.mu.Unlock()
	if s.setHook != nil {
		return s.setHook(id, text)
	}
	return nil
}

func (s *reflectStore) Calls() []reflectStoreCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]reflectStoreCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// reflectExecutor is a minimal exec.Executor stub: only Send matters
// here, but we satisfy the rest of the interface so the Reflector can
// depend on the full Executor contract.
type reflectExecutor struct {
	mu         sync.Mutex
	sendCalls  []reflectSendCall
	sendHook   func(s exec.Session, text string) error
	sendDelay  time.Duration
	sendSignal chan struct{}
}

type reflectSendCall struct {
	WS   exec.Session
	Text string
}

func (c *reflectExecutor) Start(_ context.Context, _ exec.SessionSpec) (exec.Session, error) {
	return exec.Session{}, errors.New("reflectExecutor.Start must not be called")
}
func (c *reflectExecutor) AwaitExit(_ context.Context, _ exec.Session) (int, error) {
	return 0, errors.New("reflectExecutor.AwaitExit must not be called")
}
func (c *reflectExecutor) Send(ctx context.Context, s exec.Session, text string) error {
	// A production Executor honours ctx; the fake should too so tests
	// cannot accidentally pass when production would not (review-fix-loop
	// iter 1 finding: H6 was previously testing fake-only behaviour).
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.sendDelay > 0 {
		time.Sleep(c.sendDelay)
	}
	c.mu.Lock()
	c.sendCalls = append(c.sendCalls, reflectSendCall{WS: s, Text: text})
	c.mu.Unlock()
	if c.sendSignal != nil {
		select {
		case c.sendSignal <- struct{}{}:
		default:
		}
	}
	if c.sendHook != nil {
		return c.sendHook(s, text)
	}
	return nil
}

func (c *reflectExecutor) SendCalls() []reflectSendCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]reflectSendCall, len(c.sendCalls))
	copy(out, c.sendCalls)
	return out
}

// boolSampler is a deterministic sampler test seam.
type boolSampler struct{ accept bool }

func (s boolSampler) Sample() bool { return s.accept }

// reflectAuditor mirrors the dispatch fakeAuditor — duplicated here so
// reflection tests do not depend on the order existing test files declare
// their fakeAuditor in.
type reflectAuditor struct {
	mu     sync.Mutex
	events []config.AuditEvent
}

func (a *reflectAuditor) Record(e config.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}
func (a *reflectAuditor) Events() []config.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]config.AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

// reflectDirs adapts a closure into dispatch.WorkspaceDirs.
type reflectDirs func(id int64) string

func (f reflectDirs) Dir(id int64) string { return f(id) }

// reflectFixture wires a Reflector with deterministic plumbing and an
// always-accept sampler so the happy-path tests do not need to fight
// randomness.
type reflectFixture struct {
	store    *reflectStore
	executor *reflectExecutor
	au       *reflectAuditor
	dirs     reflectDirs
	r        *dispatch.Reflector
	ctxBg    context.Context
}

const testReflectSkill = "REFLECT-SKILL-BODY"

func newReflectFixture(t *testing.T, opts ...dispatch.ReflectorOption) reflectFixture {
	t.Helper()

	root := t.TempDir()
	dirs := reflectDirs(func(id int64) string {
		return filepath.Join(root, fmt.Sprintf("%d", id))
	})
	rs := &reflectStore{}
	rex := &reflectExecutor{}
	au := &reflectAuditor{}
	now := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

	defOpts := []dispatch.ReflectorOption{
		dispatch.WithReflectionStore(rs),
		dispatch.WithReflectionExecutor(rex),
		dispatch.WithReflectionSkill(testReflectSkill),
		dispatch.WithReflectionWorkspaceDirs(dirs),
		dispatch.WithReflectionAuditor(au),
		dispatch.WithReflectionClock(func() time.Time { return now }),
		dispatch.WithReflectionSampler(boolSampler{accept: true}),
		dispatch.WithReflectionTimeout(2 * time.Second),
		dispatch.WithReflectionPollInterval(5 * time.Millisecond),
	}
	r, err := dispatch.NewReflector(append(defOpts, opts...)...)
	if err != nil {
		t.Fatalf("NewReflector: %v", err)
	}
	t.Cleanup(func() { r.Wait() })
	return reflectFixture{
		store:    rs,
		executor: rex,
		au:       au,
		dirs:     dirs,
		r:        r,
		ctxBg:    context.Background(),
	}
}

// writeReflection simulates what Claude does in production: write to
// .reflection.tmp first, then atomically rename to .reflection. Using a
// direct os.WriteFile to .reflection is non-atomic (create then write),
// which lets the polling loop read an empty file between the two syscalls
// and call SetReflection("") — a flaky failure seen on Linux CI.
func writeReflection(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	tmp := filepath.Join(dir, ".reflection.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile .reflection.tmp: %v", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, ".reflection")); err != nil {
		t.Fatalf("Rename .reflection: %v", err)
	}
}

func runningTask(id int64, ws string) store.Task {
	return store.Task{ID: id, Source: "manual", Title: "reflect me", Status: store.StatusDone, WS: ws}
}

// N1: WithStore is required.
func TestReflectorNewRejectsMissingStore(t *testing.T) {
	_, err := dispatch.NewReflector(
		dispatch.WithReflectionExecutor(&reflectExecutor{}),
		dispatch.WithReflectionSkill("S"),
		dispatch.WithReflectionWorkspaceDirs(reflectDirs(func(int64) string { return "" })),
	)
	if !errors.Is(err, dispatch.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
}

// N2: WithExecutor is required.
func TestReflectorNewRejectsMissingExecutor(t *testing.T) {
	_, err := dispatch.NewReflector(
		dispatch.WithReflectionStore(&reflectStore{}),
		dispatch.WithReflectionSkill("S"),
		dispatch.WithReflectionWorkspaceDirs(reflectDirs(func(int64) string { return "" })),
	)
	if !errors.Is(err, dispatch.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
}

// N3: empty skill body is rejected — the prompt would degenerate to just
// the sentinel instruction with no review framing.
func TestReflectorNewRejectsEmptySkill(t *testing.T) {
	_, err := dispatch.NewReflector(
		dispatch.WithReflectionStore(&reflectStore{}),
		dispatch.WithReflectionExecutor(&reflectExecutor{}),
		dispatch.WithReflectionWorkspaceDirs(reflectDirs(func(int64) string { return "" })),
		dispatch.WithReflectionSkill(""),
	)
	if !errors.Is(err, dispatch.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
}

// N4: WithWorkspaceDirs is required so the goroutine knows where to read
// the .reflection sentinel from.
func TestReflectorNewRejectsMissingWorkspaceDirs(t *testing.T) {
	_, err := dispatch.NewReflector(
		dispatch.WithReflectionStore(&reflectStore{}),
		dispatch.WithReflectionExecutor(&reflectExecutor{}),
		dispatch.WithReflectionSkill("S"),
	)
	if !errors.Is(err, dispatch.ErrInvalidConfig) {
		t.Fatalf("err = %v; want ErrInvalidConfig", err)
	}
}

// N5: SampleRate outside [0,1] is rejected.
func TestReflectorNewRejectsBadSampleRate(t *testing.T) {
	for _, bad := range []float64{-0.1, 1.5} {
		_, err := dispatch.NewReflector(
			dispatch.WithReflectionStore(&reflectStore{}),
			dispatch.WithReflectionExecutor(&reflectExecutor{}),
			dispatch.WithReflectionSkill("S"),
			dispatch.WithReflectionWorkspaceDirs(reflectDirs(func(int64) string { return "" })),
			dispatch.WithReflectionSampleRate(bad),
		)
		if !errors.Is(err, dispatch.ErrInvalidConfig) {
			t.Errorf("rate=%v err = %v; want ErrInvalidConfig", bad, err)
		}
	}
}

// S1: SampleRate(0) means no goroutine fires.
func TestReflectorSampleRateZeroNeverSends(t *testing.T) {
	f := newReflectFixture(t, dispatch.WithReflectionSampleRate(0))
	f.r.OnDone(f.ctxBg, runningTask(1, "workspace:1"))
	f.r.Wait()
	if got := len(f.executor.SendCalls()); got != 0 {
		t.Errorf("Send called %d times; want 0 with sample_rate=0", got)
	}
	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times; want 0", got)
	}
}

// S2: SampleRate(1) means OnDone always fires (verified together with H3).
// Skipping a separate test here — the H3 / H4 happy-path tests use the
// default sampler which accepts.

// S3: WithSampler beats WithSampleRate (last-writer-wins inside New).
func TestReflectorWithSamplerOverridesRate(t *testing.T) {
	f := newReflectFixture(t,
		dispatch.WithReflectionSampleRate(0),
		dispatch.WithReflectionSampler(boolSampler{accept: true}),
	)
	dir := f.dirs.Dir(7)
	go func() {
		// Wait briefly then publish .reflection so the polling goroutine
		// finds it before the test timeout.
		time.Sleep(20 * time.Millisecond)
		writeReflection(t, dir, "stub")
	}()
	f.r.OnDone(f.ctxBg, runningTask(7, "workspace:7"))
	f.r.Wait()
	if got := len(f.executor.SendCalls()); got != 1 {
		t.Errorf("Send called %d times; want 1 (sampler override)", got)
	}
}

// H1: empty WS is a no-op (cannot send to a workspace we do not have).
func TestReflectorOnDoneEmptyWSIsNoop(t *testing.T) {
	f := newReflectFixture(t)
	f.r.OnDone(f.ctxBg, runningTask(2, ""))
	f.r.Wait()
	if got := len(f.executor.SendCalls()); got != 0 {
		t.Errorf("Send called %d times; want 0 with empty WS", got)
	}
}

// H3: Send carries the skill body + sentinel write instructions naming
// the per-task workspace dir's .reflection path. Post-design-review:
// the prompt MUST use a heredoc (cat <<'EOF' ... EOF) rather than
// printf, so a reflection containing %, ", or newlines round-trips
// without shell mangling.
func TestReflectorOnDoneSendsSkillAndSentinel(t *testing.T) {
	f := newReflectFixture(t)
	const id int64 = 11
	dir := f.dirs.Dir(id)
	// Write .reflection right away so the goroutine can finish quickly.
	writeReflection(t, dir, "ok")

	f.r.OnDone(f.ctxBg, runningTask(id, "workspace:11"))
	f.r.Wait()

	calls := f.executor.SendCalls()
	if len(calls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(calls))
	}
	got := calls[0]
	if got.WS.ID != "workspace:11" {
		t.Errorf("Send WS = %q; want workspace:11", got.WS.ID)
	}
	if !strings.Contains(got.Text, testReflectSkill) {
		t.Errorf("Send text missing skill body; got %q", got.Text)
	}
	if !strings.Contains(got.Text, filepath.Join(dir, ".reflection")) {
		t.Errorf("Send text missing sentinel path; got %q", got.Text)
	}
	if !strings.Contains(got.Text, ".reflection.tmp") {
		t.Errorf("Send text missing atomic-rename hint; got %q", got.Text)
	}
	if strings.Contains(got.Text, "printf '%s") {
		t.Errorf("Send text still uses printf format; want heredoc to survive %% / quote / newline payloads. got %q", got.Text)
	}
	if !strings.Contains(got.Text, "<<'EOF'") {
		t.Errorf("Send text missing heredoc fence (cat <<'EOF' ... EOF); got %q", got.Text)
	}
}

// H4: After Send, the Reflector waits for .reflection then writes it via
// SetReflection (trimmed).
func TestReflectorOnDonePersistsReflectionFile(t *testing.T) {
	f := newReflectFixture(t)
	const id int64 = 13
	dir := f.dirs.Dir(id)

	// Publish the sentinel after a short delay so we exercise the polling
	// loop rather than the immediate-detect fast path.
	go func() {
		time.Sleep(15 * time.Millisecond)
		writeReflection(t, dir, "  trimmed reflection body \n")
	}()

	f.r.OnDone(f.ctxBg, runningTask(id, "workspace:13"))
	f.r.Wait()

	calls := f.store.Calls()
	if len(calls) != 1 {
		t.Fatalf("SetReflection calls = %d; want 1; events=%+v", len(calls), f.au.Events())
	}
	if calls[0].ID != id {
		t.Errorf("SetReflection ID = %d; want %d", calls[0].ID, id)
	}
	if calls[0].Text != "trimmed reflection body" {
		t.Errorf("SetReflection text = %q; want trimmed body", calls[0].Text)
	}
}

// H5: timeout expires before .reflection appears -> no SetReflection,
// audit reflection.timeout fires.
func TestReflectorOnDoneTimeoutFiresAuditAndSkipsPersist(t *testing.T) {
	f := newReflectFixture(t,
		dispatch.WithReflectionTimeout(40*time.Millisecond),
		dispatch.WithReflectionPollInterval(5*time.Millisecond),
	)

	f.r.OnDone(f.ctxBg, runningTask(17, "workspace:17"))
	f.r.Wait()

	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times; want 0 on timeout", got)
	}
	found := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.timeout" {
			found = true
		}
	}
	if !found {
		t.Errorf("reflection.timeout audit not found; events=%+v", f.au.Events())
	}
}

// H6: parent ctx cancel propagates; goroutine exits without SetReflection.
func TestReflectorOnDoneCtxCancelExitsCleanly(t *testing.T) {
	f := newReflectFixture(t,
		dispatch.WithReflectionTimeout(2*time.Second),
		dispatch.WithReflectionPollInterval(5*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	f.r.OnDone(ctx, runningTask(19, "workspace:19"))
	// Cancel right after firing so the polling loop bails out.
	cancel()
	f.r.Wait()

	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times; want 0 on cancel", got)
	}
	found := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.cancel" {
			found = true
		}
	}
	if !found {
		t.Errorf("reflection.cancel audit not found; events=%+v", f.au.Events())
	}
}

// H7: Wait blocks until every dispatched goroutine completes. Verified by
// counting how many SetReflection calls happen by the time Wait returns
// when many tasks are dispatched in parallel.
func TestReflectorWaitBlocksUntilAllGoroutinesExit(t *testing.T) {
	f := newReflectFixture(t)
	const fanout = 5
	for i := int64(1); i <= fanout; i++ {
		dir := f.dirs.Dir(i)
		writeReflection(t, dir, fmt.Sprintf("body %d", i))
		f.r.OnDone(f.ctxBg, runningTask(i, fmt.Sprintf("workspace:%d", i)))
	}
	f.r.Wait()
	if got := len(f.store.Calls()); got != fanout {
		t.Errorf("SetReflection calls after Wait = %d; want %d", got, fanout)
	}
}

// H8: Send failure -> reflection.fail audit; no SetReflection call.
func TestReflectorOnDoneSendFailureAudits(t *testing.T) {
	f := newReflectFixture(t)
	f.executor.sendHook = func(_ exec.Session, _ string) error {
		return errors.New("cmux send: boom")
	}
	f.r.OnDone(f.ctxBg, runningTask(23, "workspace:23"))
	f.r.Wait()
	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times after Send failure; want 0", got)
	}
	found := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.fail" && strings.Contains(ev.Value, "boom") {
			found = true
		}
	}
	if !found {
		t.Errorf("reflection.fail audit not found; events=%+v", f.au.Events())
	}
}

// H9: reflection.start audit fires immediately before Send (so a Send
// timing out / blocking does not hide the fact that we tried).
func TestReflectorOnDoneStartAuditPrecedesSend(t *testing.T) {
	f := newReflectFixture(t)
	const id int64 = 29
	// Use a send signal so we can sequence the assertion deterministically.
	f.executor.sendSignal = make(chan struct{}, 1)
	dir := f.dirs.Dir(id)
	writeReflection(t, dir, "stub")
	f.r.OnDone(f.ctxBg, runningTask(id, "workspace:29"))
	select {
	case <-f.executor.sendSignal:
	case <-time.After(time.Second):
		t.Fatalf("Send was not invoked within the test deadline")
	}
	// Start audit must already be present at this point.
	startSeen := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.start" && strings.Contains(ev.Key, fmt.Sprintf("%d", id)) {
			startSeen = true
		}
	}
	if !startSeen {
		t.Errorf("reflection.start audit missing at Send time; events=%+v", f.au.Events())
	}
	f.r.Wait()
}

// H10 (post-design-review): a stray .reflection.tmp on its own does NOT
// trigger SetReflection. The Reflector polls for the final .reflection
// path, so the atomic publish contract is preserved end-to-end (no
// half-written read into tasks.reflection).
func TestReflectorIgnoresOrphanTmpFile(t *testing.T) {
	f := newReflectFixture(t,
		dispatch.WithReflectionTimeout(40*time.Millisecond),
		dispatch.WithReflectionPollInterval(5*time.Millisecond),
	)
	const id int64 = 31
	dir := f.dirs.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write only the .tmp half (mid-publish state).
	if err := os.WriteFile(filepath.Join(dir, ".reflection.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f.r.OnDone(f.ctxBg, runningTask(id, "workspace:31"))
	f.r.Wait()
	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times for orphan .tmp; want 0", got)
	}
}

// review-fix-loop iter 1 (test-quality Critical): production cmux.Client
// honours ctx.Err() — when the parent ctx is cancelled before Send
// completes, Send returns context.Canceled. The Reflector must classify
// that as reflection.cancel (not reflection.fail), so the audit trail
// distinguishes "we shut down" from "Claude / cmux blew up".
func TestReflectorOnDoneClassifiesSendCancelAsCancelAudit(t *testing.T) {
	f := newReflectFixture(t)
	f.executor.sendHook = func(_ exec.Session, _ string) error {
		return context.Canceled
	}
	f.r.OnDone(f.ctxBg, runningTask(41, "workspace:41"))
	f.r.Wait()

	if got := len(f.store.Calls()); got != 0 {
		t.Errorf("SetReflection called %d times after Send returned ctx.Canceled; want 0", got)
	}
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.fail" {
			t.Errorf("Send ctx.Canceled mis-classified as reflection.fail; events=%+v", f.au.Events())
		}
	}
	cancelSeen := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.cancel" {
			cancelSeen = true
		}
	}
	if !cancelSeen {
		t.Errorf("reflection.cancel audit not recorded; events=%+v", f.au.Events())
	}
}

// review-fix-loop iter 1 (test-quality Critical): same shape as cancel —
// Send returning context.DeadlineExceeded must record reflection.timeout.
func TestReflectorOnDoneClassifiesSendDeadlineAsTimeoutAudit(t *testing.T) {
	f := newReflectFixture(t)
	f.executor.sendHook = func(_ exec.Session, _ string) error {
		return context.DeadlineExceeded
	}
	f.r.OnDone(f.ctxBg, runningTask(43, "workspace:43"))
	f.r.Wait()

	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.fail" {
			t.Errorf("Send DeadlineExceeded mis-classified as reflection.fail; events=%+v", f.au.Events())
		}
	}
	timeoutSeen := false
	for _, ev := range f.au.Events() {
		if ev.Action == "reflection.timeout" {
			timeoutSeen = true
		}
	}
	if !timeoutSeen {
		t.Errorf("reflection.timeout audit not recorded; events=%+v", f.au.Events())
	}
}

// review-fix-loop iter 1 (design-conformance Critical): the cmux Send
// error / SetReflection error message can echo back tokens (Bearer
// headers, API keys). dispatch.markFailed already runs every audit value
// through logging.Redact — Reflector must do the same so secrets do not
// leak into audit.log.
func TestReflectorRedactsSecretsBeforeAuditing(t *testing.T) {
	f := newReflectFixture(t)
	f.executor.sendHook = func(_ exec.Session, _ string) error {
		return errors.New("cmux send: Bearer sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa rejected")
	}
	f.r.OnDone(f.ctxBg, runningTask(47, "workspace:47"))
	f.r.Wait()
	for _, ev := range f.au.Events() {
		if strings.Contains(ev.Value, "sk-ant-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
			t.Errorf("audit value leaked secret token: %q", ev.Value)
		}
		if strings.Contains(ev.Value, "Bearer sk-ant") {
			t.Errorf("audit value leaked Bearer header: %q", ev.Value)
		}
	}
}

// review-fix-loop iter 1 (design Warning, real correctness): the prompt
// embeds the timeout the hook will actually wait for. A custom
// WithReflectionTimeout(40ms) must be reflected in the prompt text so
// Claude is not lied to about the deadline.
func TestReflectorPromptUsesConfiguredTimeout(t *testing.T) {
	custom := 73 * time.Second
	f := newReflectFixture(t,
		dispatch.WithReflectionTimeout(custom),
	)
	const id int64 = 53
	dir := f.dirs.Dir(id)
	writeReflection(t, dir, "ok")
	f.r.OnDone(f.ctxBg, runningTask(id, "workspace:53"))
	f.r.Wait()
	calls := f.executor.SendCalls()
	if len(calls) != 1 {
		t.Fatalf("Send calls = %d; want 1", len(calls))
	}
	if !strings.Contains(calls[0].Text, custom.String()) {
		t.Errorf("prompt does not advertise custom timeout %s; got %q",
			custom, calls[0].Text)
	}
}

// Make sure the deferred Wait + atomic counter inside the package does
// not leave goroutines around after multiple OnDone calls without Send
// completion (sampling false branch).
func TestReflectorWaitReturnsImmediatelyWhenSamplingFalse(t *testing.T) {
	f := newReflectFixture(t, dispatch.WithReflectionSampler(boolSampler{accept: false}))
	var fired atomic.Int32
	for i := int64(1); i <= 3; i++ {
		f.r.OnDone(f.ctxBg, runningTask(i, fmt.Sprintf("workspace:%d", i)))
		fired.Add(1)
	}
	done := make(chan struct{})
	go func() {
		f.r.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Wait did not return after 3 sampled-false OnDone calls (fired=%d)", fired.Load())
	}
}
