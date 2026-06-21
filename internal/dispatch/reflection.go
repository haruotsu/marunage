package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/store"
)

// Reflector is the PR-102 completion hook. It is invoked from the
// completion watcher (see internal/completion.WithDoneHook) immediately
// after a row flips to done. For the configured sample of completions
// it sends the marunage-reflect SKILL into the same cmux workspace and
// waits, asynchronously, for Claude to publish a `.reflection` sentinel
// in the workspace directory. When the file appears the trimmed body is
// persisted via store.SetReflection so a follow-up `marunage show` /
// Web UI render surfaces the answer next to the result_summary.
//
// The hook fires in a fresh goroutine so the watcher's tick stays
// responsive. Wait blocks until every dispatched goroutine completes,
// which the daemon shutdown path uses to drain in-flight reflections
// before exiting.
type Reflector struct {
	store        ReflectionStore
	executor     exec.Executor
	dirs         WorkspaceDirs
	skill        string
	auditor      config.Auditor
	now          func() time.Time
	sampler      Sampler
	timeout      time.Duration
	pollInterval time.Duration

	wg sync.WaitGroup
}

// ReflectionStore is the narrow read/write surface the Reflector needs
// against the tasks table. Production wires *store.TaskRepo; tests inject
// a fake. The Reflector takes the post-done store.Task as input from the
// completion watcher and never re-fetches, so SetReflection is the only
// method here — adding a Get just for "future flexibility" is YAGNI and
// only widens what fake implementations have to satisfy.
type ReflectionStore interface {
	SetReflection(ctx context.Context, id int64, text string) error
}

// Sampler decides whether a given completion should trigger a reflection
// run. Returning false short-circuits OnDone before any cmux work fires.
// The default implementation is a rate-based random sampler; tests
// inject deterministic alwaysTrue / alwaysFalse stubs.
type Sampler interface {
	Sample() bool
}

// rateSampler accepts with probability rate. Uses math/rand/v2 (the
// post-1.22 PRNG) so fresh process invocations vary without seeding.
// rate=0 short-circuits to never-fire so the no-cost path stays cheap;
// rate=1 short-circuits to always-fire so a deterministic [reflection]
// rate=1 config never silently drops a completion to PRNG variance.
type rateSampler struct {
	rate float64
	rng  *rand.Rand
	mu   sync.Mutex
}

// newRateSampler seeds the PRNG from the injected clock (r.now), so a
// test using WithReflectionClock pins both the audit timestamp source
// and the sampler's draw sequence to one place. Production callers use
// time.Now and get a different seed every process start.
func newRateSampler(rate float64, now func() time.Time) *rateSampler {
	if now == nil {
		now = time.Now
	}
	return &rateSampler{
		rate: rate,
		rng:  rand.New(rand.NewPCG(uint64(now().UnixNano()), 0xC0FFEE)),
	}
}

func (s *rateSampler) Sample() bool {
	switch {
	case s.rate <= 0:
		return false
	case s.rate >= 1:
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64() < s.rate
}

// ReflectorOption mutates Reflector construction.
type ReflectorOption func(*reflectorBuild)

// samplerSource records which sampler option was applied last so
// NewReflector can implement last-writer-wins precedence between
// WithReflectionSampleRate and WithReflectionSampler. The last option
// supplied to NewReflector wins, mirroring how every other functional
// option in this package behaves (e.g. WithCmux overwriting a prior
// WithCmux).
type samplerSource int

const (
	samplerSourceNone samplerSource = iota
	samplerSourceRate
	samplerSourceExplicit
)

// reflectorBuild collects the construction-time inputs so we can apply
// the SampleRate / Sampler precedence rule cleanly inside NewReflector.
type reflectorBuild struct {
	r              *Reflector
	sampleRate     float64
	sampleRateSet  bool
	explicitSample Sampler
	last           samplerSource
}

// WithReflectionStore injects the tasks-table repository. Required.
func WithReflectionStore(s ReflectionStore) ReflectorOption {
	return func(b *reflectorBuild) { b.r.store = s }
}

// WithReflectionExecutor injects the execution backend used to send the
// reflect prompt back into the same session. Required.
func WithReflectionExecutor(e exec.Executor) ReflectorOption {
	return func(b *reflectorBuild) { b.r.executor = e }
}

// WithReflectionWorkspaceDirs injects the per-task control-directory
// resolver shared with the dispatcher and completion watcher. The
// goroutine reads the resulting `<dir>/.reflection` sentinel.
func WithReflectionWorkspaceDirs(d WorkspaceDirs) ReflectorOption {
	return func(b *reflectorBuild) { b.r.dirs = d }
}

// WithReflectionSkill installs the marunage-reflect SKILL.md body. The
// hook concatenates this body with a sentinel-write instruction before
// passing the result to the executor's Send.
func WithReflectionSkill(s string) ReflectorOption {
	return func(b *reflectorBuild) { b.r.skill = s }
}

// WithReflectionAuditor installs the audit-log sink. Defaults to
// config.NopAuditor so callers that have not yet wired audit.log keep
// building.
func WithReflectionAuditor(a config.Auditor) ReflectorOption {
	return func(b *reflectorBuild) { b.r.auditor = a }
}

// WithReflectionClock injects a deterministic clock for tests / future
// time-stamped audit fields. Defaults to time.Now.
func WithReflectionClock(now func() time.Time) ReflectorOption {
	return func(b *reflectorBuild) { b.r.now = now }
}

// WithReflectionSampleRate installs a probability in [0,1]. Validated
// in NewReflector. Last-writer-wins against WithReflectionSampler:
// whichever option is supplied later in the NewReflector argument list
// determines the effective sampler.
func WithReflectionSampleRate(r float64) ReflectorOption {
	return func(b *reflectorBuild) {
		b.sampleRate = r
		b.sampleRateSet = true
		b.last = samplerSourceRate
	}
}

// WithReflectionSampler injects an explicit Sampler — useful in tests
// (alwaysTrue / alwaysFalse) and for future strategies (deterministic
// hash of task id, time-of-day weighting, etc.). Last-writer-wins
// against WithReflectionSampleRate.
func WithReflectionSampler(s Sampler) ReflectorOption {
	return func(b *reflectorBuild) {
		b.explicitSample = s
		b.last = samplerSourceExplicit
	}
}

// WithReflectionTimeout caps how long one OnDone goroutine waits for the
// .reflection sentinel before giving up. Defaults to 5m.
func WithReflectionTimeout(d time.Duration) ReflectorOption {
	return func(b *reflectorBuild) { b.r.timeout = d }
}

// WithReflectionPollInterval overrides how often the goroutine stats the
// sentinel file. Defaults to 1s; tests squash to a few milliseconds.
func WithReflectionPollInterval(d time.Duration) ReflectorOption {
	return func(b *reflectorBuild) { b.r.pollInterval = d }
}

// Audit action labels. Mirrors the dispatcher's "dispatch.<verb>" naming
// so a single audit.log line filter matches the whole reflection
// lifecycle.
const (
	auditReflectionStart   = "reflection.start"
	auditReflectionDone    = "reflection.done"
	auditReflectionFail    = "reflection.fail"
	auditReflectionTimeout = "reflection.timeout"
	auditReflectionCancel  = "reflection.cancel"
)

// Defaults. A 5-minute window is generous for a Claude reflection run
// (typically 10-30 seconds); a 1-second poll keeps the SQLite WAL quiet
// in the no-op case (no .reflection yet) without lagging detection.
const (
	defaultReflectionTimeout      = 5 * time.Minute
	defaultReflectionPollInterval = 1 * time.Second
	defaultReflectionSampleRate   = 1.0

	reflectionFile          = ".reflection"
	reflectionMaxBytes      = 64 * 1024
	reflectionAuditValueMax = 512
)

// NewReflector validates required options and returns a Reflector ready
// to receive OnDone calls.
func NewReflector(opts ...ReflectorOption) (*Reflector, error) {
	r := &Reflector{
		auditor:      config.NopAuditor{},
		now:          time.Now,
		timeout:      defaultReflectionTimeout,
		pollInterval: defaultReflectionPollInterval,
	}
	b := &reflectorBuild{r: r, sampleRate: defaultReflectionSampleRate}
	for _, opt := range opts {
		opt(b)
	}
	if r.store == nil {
		return nil, fmt.Errorf("%w: WithReflectionStore", ErrInvalidConfig)
	}
	if r.executor == nil {
		return nil, fmt.Errorf("%w: WithReflectionExecutor", ErrInvalidConfig)
	}
	if r.dirs == nil {
		return nil, fmt.Errorf("%w: WithReflectionWorkspaceDirs", ErrInvalidConfig)
	}
	if strings.TrimSpace(r.skill) == "" {
		return nil, fmt.Errorf("%w: WithReflectionSkill (must be non-empty)", ErrInvalidConfig)
	}
	if r.timeout <= 0 {
		r.timeout = defaultReflectionTimeout
	}
	if r.pollInterval <= 0 {
		r.pollInterval = defaultReflectionPollInterval
	}
	if b.sampleRateSet && (b.sampleRate < 0 || b.sampleRate > 1) {
		return nil, fmt.Errorf("%w: WithReflectionSampleRate=%v (want [0,1])", ErrInvalidConfig, b.sampleRate)
	}
	switch b.last {
	case samplerSourceExplicit:
		r.sampler = b.explicitSample
	case samplerSourceRate:
		r.sampler = newRateSampler(b.sampleRate, r.now)
	default:
		r.sampler = newRateSampler(defaultReflectionSampleRate, r.now)
	}
	return r, nil
}

// OnDone is the post-done hook. It samples; on accept it spawns a
// goroutine that sends the reflection prompt and waits for the
// `.reflection` sentinel to land in the per-task workspace dir. The
// caller must call Wait before exiting the process to drain in-flight
// reflections.
//
// Empty task.WS is a no-op: there is no cmux session to send into.
func (r *Reflector) OnDone(parent context.Context, task store.Task) {
	if task.WS == "" {
		return
	}
	if !r.sampler.Sample() {
		return
	}
	r.wg.Add(1)
	go r.runOne(parent, task)
}

// Wait blocks until every OnDone-spawned goroutine exits. Daemon
// shutdown should call Wait before closing the cmux connection so an
// in-flight reflection has a chance to finish (or hit its own timeout).
func (r *Reflector) Wait() { r.wg.Wait() }

// runOne is the per-task body. The fresh ctx is the parent ctx capped by
// the per-call timeout so a misbehaving Claude session cannot hold a
// goroutine open past the configured cap.
func (r *Reflector) runOne(parent context.Context, task store.Task) {
	defer r.wg.Done()

	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()

	dir := r.dirs.Dir(task.ID)
	prompt := r.buildReflectionPrompt(dir)

	r.recordAudit(auditReflectionStart, task.ID, task.WS)

	if err := r.executor.Send(ctx, exec.NewSession(task.WS, nil), prompt); err != nil {
		// Distinguish ctx-derived cancel / timeout from genuine backend
		// failures so audit.log can tell "we shut down" / "Claude was
		// too slow" apart from "the backend blew up". A well-behaved
		// Executor returns ctx.Err() when ctx fires mid-Send.
		r.classifyCtxOrFail(task.ID, err, "executor send failed")
		return
	}

	body, waitErr := r.waitForSentinel(ctx, dir)
	if waitErr != nil {
		r.classifyCtxOrFail(task.ID, waitErr, "read "+reflectionFile+" failed")
		return
	}

	if err := r.store.SetReflection(ctx, task.ID, body); err != nil {
		r.classifyCtxOrFail(task.ID, err, "SetReflection failed")
		return
	}
	r.recordAudit(auditReflectionDone, task.ID,
		fmt.Sprintf("len=%d", len(body)))
}

// classifyCtxOrFail centralises the "is this a ctx-driven exit or a
// real failure?" decision so every error site (Send / wait / persist)
// branches identically. context.DeadlineExceeded -> reflection.timeout,
// context.Canceled -> reflection.cancel, anything else ->
// reflection.fail. The descriptive prefix is glued onto the wrapped
// error so the audit value tells operators *which* step bailed.
func (r *Reflector) classifyCtxOrFail(taskID int64, err error, prefix string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		r.recordAudit(auditReflectionTimeout, taskID,
			fmt.Sprintf("%s: waited up to %s for %s", prefix, r.timeout, reflectionFile))
	case errors.Is(err, context.Canceled):
		r.recordAudit(auditReflectionCancel, taskID,
			fmt.Sprintf("%s: parent context cancelled", prefix))
	default:
		r.recordAudit(auditReflectionFail, taskID,
			fmt.Sprintf("%s: %v", prefix, err))
	}
}

// waitForSentinel polls dir/.reflection until it appears, ctx fires, or
// a non-recoverable I/O error surfaces. The contents are returned
// trimmed; an empty file resolves to "" (the SetReflection helper clears
// the column rather than leaving stale text behind).
func (r *Reflector) waitForSentinel(ctx context.Context, dir string) (string, error) {
	path := filepath.Join(dir, reflectionFile)

	// Probe once before sleeping so a sentinel that landed during the
	// Send round-trip is detected without waiting a poll interval.
	if data, err := readReflectionFile(path); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			data, err := readReflectionFile(path)
			if err == nil {
				return strings.TrimSpace(string(data)), nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
}

// readReflectionFile mirrors completion.readBoundedNoFollow's safety
// stance (O_NOFOLLOW + bounded read) so a Claude session that swaps the
// sentinel for a symlink to /etc cannot leak host bytes through
// tasks.reflection. Kept private to this package so the dispatcher and
// completion packages do not have to share an I/O helper they would
// version-skew over.
func readReflectionFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("refused symlink at %s", filepath.Base(path))
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > reflectionMaxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), reflectionMaxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, reflectionMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > reflectionMaxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), reflectionMaxBytes)
	}
	return data, nil
}

// buildReflectionPrompt concatenates the SKILL body and the sentinel
// write instruction. Empty workspaceDir collapses to just the skill
// (no place to publish the sentinel) — callers should not invoke
// OnDone in that state, but defending here keeps the helper testable.
//
// The publish snippet uses a single-quoted heredoc (cat <<'EOF') rather
// than printf so a reflection containing %, ", \, or embedded newlines
// is delivered to disk verbatim — printf '%s' would mangle %-prefixed
// substrings and quote nesting would silently lose lines (design-review
// finding from go-design / security agents).
//
// The advertised timeout / size cap comes from the receiver so an
// operator who tightened WithReflectionTimeout / future per-Reflector
// caps does not hand Claude a misleading deadline (review-fix-loop
// finding: prompt body and Reflector were two SSOTs).
func (r *Reflector) buildReflectionPrompt(workspaceDir string) string {
	body := strings.TrimSpace(r.skill)
	if workspaceDir == "" {
		return body
	}
	finalPath := filepath.Join(workspaceDir, reflectionFile)
	tmpPath := filepath.Join(workspaceDir, reflectionFile+".tmp")
	return body + "\n\n## Reflection sentinel (auto-injected)\n\n" +
		"After writing your review, publish it atomically so the marunage " +
		"reflection hook can read a complete file. The hook waits up to " +
		r.timeout.String() + " for the file to appear and reads at most " +
		fmt.Sprintf("%d KiB", reflectionMaxBytes/1024) + " of plain UTF-8 text " +
		"(symlinks and oversized files are rejected). Use a heredoc so the " +
		"body survives quotes / newlines / percent signs:\n\n" +
		"  cat > " + tmpPath + " <<'EOF'\n" +
		"  <your full reflection here, free-form text>\n" +
		"  EOF\n" +
		"  mv " + tmpPath + " " + finalPath + "\n\n" +
		"Do not write " + finalPath + " directly; always go through the .tmp " +
		"+ mv so the reader never sees a half-written file. Do not include " +
		"secrets (API keys, tokens, raw credentials) in the body — the value " +
		"is rendered in the marunage Web UI."
}

// recordAudit is the single sink for audit events fired by this
// package. Every Value runs through logging.Redact (so a Bearer header /
// API key echoed back from cmux stderr cannot pin to audit.log) and is
// then bounded so a 100KiB error payload does not bloat the trail.
// dispatch.markFailed already follows this pattern; mirroring it here
// keeps the dispatcher and the reflection hook on the same secrets-out
// guarantee.
func (r *Reflector) recordAudit(action string, id int64, value string) {
	r.auditor.Record(config.AuditEvent{
		Action: action,
		Key:    "task:" + strconv.FormatInt(id, 10),
		Value:  truncateAuditValue(logging.Redact(value), reflectionAuditValueMax),
	})
}

func truncateAuditValue(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
