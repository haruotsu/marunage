// Package completion is the atomic-sentinel completion detector marunage
// promises in docs/requirement.md "Crash safety" (invariant #5) and
// "atomic sentinel による完了検知". It owns the second half of the Act
// phase that PR-42's dispatcher hands off: once a row has flipped to
// running and Claude is loose inside the cmux workspace, this package
// polls the per-task workspace directory for the sentinel file the
// prompt instructed Claude to write at exit.
//
// Wire shape:
//
//   - The dispatcher (PR-42 + PR-43 wiring) creates ~/.marunage/workspaces/<id>/
//     and embeds the path in the prompt as "echo $? > .exit_code.tmp &&
//     mv .exit_code.tmp .exit_code". `mv` on the same filesystem is
//     atomic, so a reader either sees the final byte or no file at all.
//   - The Watcher polls store.List(Statuses=[running]) on each tick,
//     stats <dir>/.exit_code, and on success transitions the row to
//     done (with a result_summary lifted from <dir>/.result_summary).
//     A non-zero exit code or a malformed sentinel surfaces as failed
//     with judgment_reason, plus a completion.fail audit entry.
//
// What this package does NOT cover (deferred to PR-44 reaper):
//   - cmux workspace deleted out from under us. Per task spec, "sentinel
//     未到達でワークスペースが消えていた場合は PR-44 reaper の責務 — 本
//     PR では running 維持で良い (失敗扱いしない)". The watcher therefore
//     no-ops when the workspace dir is missing rather than failing the row.
//   - "started_at + 24h" stuck-timeout probe.
//
// Tests (internal/completion/completion_test.go) drive every branch via
// a per-test temp directory, so the package never assumes the real
// ~/.marunage/workspaces/ tree exists.
package completion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/store"
)

// Store is the narrow read/write surface the Watcher needs against the
// tasks table. Same test-seam pattern as dispatch.Store: production
// wires *store.TaskRepo, tests inject a fake. The method set is
// intentionally a subset of *store.TaskRepo so the concrete type
// satisfies it implicitly.
type Store interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
	MarkDoneWithSummary(ctx context.Context, id int64, summary string, completedAt time.Time) error
	MarkFailedWithReason(ctx context.Context, id int64, reason string) error
	SetCompletedAt(ctx context.Context, id int64, t time.Time) error
}

// WorkspaceDirs resolves the per-task on-disk directory marunage owns
// (separate from cmux's workspace and from task.CWD). Used by both the
// dispatcher (to embed the sentinel write path in the prompt) and the
// watcher (to read the sentinel back). Production wires a function
// rooted at ~/.marunage/workspaces/<id>; tests use t.TempDir() per task.
type WorkspaceDirs interface {
	Dir(taskID int64) string
}

// Sentinel filenames. Kept private so the dispatcher and watcher agree
// through this package; callers compose them via WorkspaceDirs.Dir(id).
const (
	sentinelFile      = ".exit_code"
	resultSummaryFile = ".result_summary"
)

// Read caps + display caps. The watcher reads two attacker-influenced
// files (Claude can write whatever it wants in the workspace dir) and
// surfaces their bytes through audit.log + judgment_reason — both
// readable through the Web UI. Without bounds, a prompt-injected Claude
// can leak arbitrary file contents (via symlink) or stuff the audit
// trail with megabytes of garbage.
//
// Read caps are the I/O-time defence (refuse to slurp huge files).
// Display caps are the rendering-time defence (truncate the rejected
// raw bytes before they reach the audit Value / judgment_reason).
const (
	maxSentinelBytes      = 64        // exit_code is "0\n", "127\n", etc. — pad for whitespace
	maxResultSummaryBytes = 64 * 1024 // 64 KiB of human summary is generous
	maxRawDisplayBytes    = 64        // truncate rejected raw bytes before logging
)

// Audit action labels. requirement.md invariant #2 "No silent execution"
// requires every status transition to leave an audit trail; PR-43 owns
// the running -> done / failed half of that ledger.
//
// The split between auditFail and auditTransitionFailed is deliberate:
//   - auditFail means "the row actually moved to failed" (Claude
//     reported a non-zero exit, parse failure, rejected sentinel).
//   - auditTransitionFailed means "the watcher tried to transition the
//     row but the underlying store write was rejected" — the row stays
//     running and the next tick retries.
//
// Conflating the two would make every store hiccup look like a Claude
// failure in `marunage review`, breaking forensic value.
const (
	auditDetect           = "completion.detect"
	auditFail             = "completion.fail"
	auditTransitionFailed = "completion.transition_failed"
)

// Default poll cadence. 5s matches the task spec's "周期は config 経由
// で可変、デフォルト 5s 程度". Short enough that an interactive
// `marunage status --watch` reflects completion within the typical
// human glance cycle, long enough not to flood the SQLite WAL with
// List queries during quiet periods.
const defaultPollInterval = 5 * time.Second

// ErrInvalidConfig signals a missing required Option at construction.
// Mirrors dispatch.ErrInvalidConfig so a buggy CLI wiring fails loud
// at startup with a typed sentinel callers can errors.Is against.
var ErrInvalidConfig = errors.New("completion: missing required option")

// Watcher polls running tasks for sentinel completion files and drives
// status transitions. Safe for one Run goroutine per process; the
// underlying Store + Auditor must themselves be concurrency-safe (the
// production *store.TaskRepo and *logging.AuditLog already are).
type Watcher struct {
	store        Store
	dirs         WorkspaceDirs
	auditor      config.Auditor
	pollInterval time.Duration
	now          func() time.Time
}

// Option mutates Watcher construction. The functional-option shape
// matches dispatch.Option and store.Option so the package surface stays
// uniform across the execution layer.
type Option func(*Watcher)

// WithStore injects the tasks-table repository. Required.
func WithStore(s Store) Option { return func(w *Watcher) { w.store = s } }

// WithWorkspaceDirs injects the per-task directory resolver. Required.
// Production wraps a closure rooted at ~/.marunage/workspaces; tests
// hand back t.TempDir() subdirectories.
func WithWorkspaceDirs(d WorkspaceDirs) Option {
	return func(w *Watcher) { w.dirs = d }
}

// WithAuditor installs the audit-log sink. Defaults to config.NopAuditor
// so existing tests / CLI paths that have not yet wired audit.log keep
// building. Production wires the same *logging.AuditLog the dispatcher
// already opens, so completion entries land in the same audit.log as
// dispatch.start / dispatch.fail.
func WithAuditor(a config.Auditor) Option {
	return func(w *Watcher) { w.auditor = a }
}

// WithPollInterval overrides the 5s default. Tests squash this to a
// few milliseconds so Run-loop tests finish in well under a second.
func WithPollInterval(d time.Duration) Option {
	return func(w *Watcher) { w.pollInterval = d }
}

// WithClock injects a deterministic clock used to stamp completed_at.
// Defaults to time.Now in production. The Run-loop polling cadence
// still depends on the real wall clock through time.NewTicker — this
// option pins the timestamp decision but does not eliminate sleep-based
// timing for the loop itself.
func WithClock(now func() time.Time) Option {
	return func(w *Watcher) { w.now = now }
}

// New builds a Watcher. Required: WithStore, WithWorkspaceDirs.
// Returns ErrInvalidConfig naming the missing field so a buggy CLI
// wiring fails loud at startup.
func New(opts ...Option) (*Watcher, error) {
	w := &Watcher{
		auditor:      config.NopAuditor{},
		pollInterval: defaultPollInterval,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(w)
	}
	if w.store == nil {
		return nil, fmt.Errorf("%w: WithStore", ErrInvalidConfig)
	}
	if w.dirs == nil {
		return nil, fmt.Errorf("%w: WithWorkspaceDirs", ErrInvalidConfig)
	}
	if w.pollInterval <= 0 {
		w.pollInterval = defaultPollInterval
	}
	return w, nil
}

// Run polls every running task's workspace for sentinel completion
// until ctx is cancelled. The first Tick fires immediately so a
// pre-existing sentinel is detected without a poll-interval wait;
// subsequent ticks fire on the configured cadence.
//
// Context cancellation returns nil (clean shutdown for daemon callers).
// Per-tick errors are logged via the audit trail when they cause a row
// transition; transient infrastructure errors (Store.List blew up) are
// returned so the daemon caller can decide whether to keep going or
// fail loud.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.Tick(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.Tick(ctx); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return err
			}
		}
	}
}

// Tick scans every running row exactly once. Per-row failures (sentinel
// parse error, Store update failure) do not abort the scan — the next
// row is checked regardless, mirroring the dispatcher's "巻き込み故障
// させない" stance from requirement.md.
//
// The only error Tick returns is a Store.List failure: without the
// candidate set the scan cannot proceed at all, so surfacing the error
// lets Run decide whether to retry or shut down.
func (w *Watcher) Tick(ctx context.Context) error {
	candidates, err := w.store.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusRunning},
	})
	if err != nil {
		return fmt.Errorf("completion: list running: %w", err)
	}
	for _, task := range candidates {
		w.checkOne(ctx, task)
	}
	return nil
}

// checkOne probes a single running row for its sentinel and dispatches
// the resulting transition. All failures here are recorded onto the row
// (or audit.log) rather than returned so one stuck workspace does not
// poison the rest of the scan.
func (w *Watcher) checkOne(ctx context.Context, task store.Task) {
	dir := w.dirs.Dir(task.ID)

	// Defence-in-depth: O_NOFOLLOW only protects the FINAL path
	// component. If <dir> itself is a symlink (Claude does
	// `rm -rf <dir> && ln -s /etc <dir>` mid-task), the kernel
	// resolves <dir>/.exit_code through the symlink and we'd read
	// /etc/.exit_code. Lstat the dir first and refuse to descend
	// when it is not a real directory.
	if info, err := os.Lstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			w.markFailed(ctx, task.ID,
				fmt.Sprintf("completion: workspace dir rejected (refused to follow symlink at %s)", filepath.Base(dir)))
			return
		}
		if !info.IsDir() {
			w.markFailed(ctx, task.ID,
				fmt.Sprintf("completion: workspace dir rejected (not a directory at %s)", filepath.Base(dir)))
			return
		}
	}

	data, err := readBoundedNoFollow(filepath.Join(dir, sentinelFile), maxSentinelBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Steady state for an in-flight task. PR-44 reaper owns the
			// "workspace deleted entirely" branch.
			return
		}
		if errors.Is(err, errSymlinkRefused) {
			w.markFailed(ctx, task.ID,
				fmt.Sprintf("completion: %s rejected (refused to follow symlink)", sentinelFile))
			return
		}
		if errors.Is(err, errNotRegularFile) {
			w.markFailed(ctx, task.ID,
				fmt.Sprintf("completion: %s rejected (not a regular file)", sentinelFile))
			return
		}
		if errors.Is(err, errFileTooLarge) {
			w.markFailed(ctx, task.ID,
				fmt.Sprintf("completion: %s rejected (exceeds %d bytes)", sentinelFile, maxSentinelBytes))
			return
		}
		// Other I/O errors (transient permission denied, etc.) — leave
		// the row running so the next tick retries.
		return
	}

	rawSentinel := strings.TrimSpace(string(data))
	exitCode, parseErr := strconv.Atoi(rawSentinel)
	if parseErr != nil {
		reason := fmt.Sprintf("completion: parse %s failed: %v (raw=%q)",
			sentinelFile, parseErr, truncateForDisplay(rawSentinel, maxRawDisplayBytes))
		w.markFailed(ctx, task.ID, reason)
		return
	}
	if exitCode != 0 {
		reason := fmt.Sprintf("completion: claude exited non-zero exit_code=%d", exitCode)
		w.markFailed(ctx, task.ID, reason)
		return
	}

	summary := w.readResultSummary(dir)
	if err := w.store.MarkDoneWithSummary(ctx, task.ID, summary, w.now()); err != nil {
		// MarkDoneWithSummary failed mid-transition — leave the row running
		// so the next tick can retry. Record under
		// completion.transition_failed (NOT completion.fail) so the audit
		// label matches reality: no state flipped.
		w.recordAudit(auditTransitionFailed, task.ID,
			fmt.Sprintf("completion: MarkDoneWithSummary failed: %v", err))
		return
	}
	w.recordAudit(auditDetect, task.ID, strconv.Itoa(exitCode))
}

// markFailed flips the row to failed, stamps completed_at, and records
// the audit entry. SetCompletedAt is best-effort (a missing stamp is
// less harmful than losing the failed transition itself); audit.log
// always records the reason verbatim so post-mortem can reconstruct.
//
// MarkFailedWithReason failure: the row did NOT transition, so the
// audit action is auditTransitionFailed (not auditFail) — same label
// reasoning as the MarkDoneWithSummary failure path in checkOne.
func (w *Watcher) markFailed(ctx context.Context, id int64, reason string) {
	if err := w.store.MarkFailedWithReason(ctx, id, reason); err != nil {
		w.recordAudit(auditTransitionFailed, id,
			fmt.Sprintf("MarkFailedWithReason failed: %v", err))
		return
	}
	// Stamp completed_at so dashboards don't render the row as "still
	// running". Failure here is logged but not fatal — the row is at
	// least correctly flagged as failed.
	_ = w.store.SetCompletedAt(ctx, id, w.now())
	w.recordAudit(auditFail, id, reason)
}

// readResultSummary returns the trimmed contents of <dir>/.result_summary
// or "" when the file is absent OR fails the security gate (symlink,
// non-regular, oversized). The summary is optional — the watcher must
// not fail the row when its only problem is a missing/malformed
// summary file, but it must also never leak symlink-target contents
// into tasks.result_summary (Web UI surface).
func (w *Watcher) readResultSummary(dir string) string {
	data, err := readBoundedNoFollow(filepath.Join(dir, resultSummaryFile), maxResultSummaryBytes)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Sentinel file-read errors. Distinct sentinels so checkOne can render
// each rejection reason without leaking attacker-controlled bytes from
// the file's name / target.
var (
	errSymlinkRefused = errors.New("completion: refused to follow symlink")
	errNotRegularFile = errors.New("completion: not a regular file")
	errFileTooLarge   = errors.New("completion: file exceeds size cap")
)

// readBoundedNoFollow opens path with O_NOFOLLOW (rejects symlinks at
// the syscall level — no TOCTOU window between Lstat and Open) and
// reads at most maxBytes+1 bytes. If the file exceeds maxBytes the
// excess byte triggers errFileTooLarge before the over-read can bloat
// memory or downstream audit logs.
//
// Defence-in-depth notes:
//   - O_NOFOLLOW returns ELOOP on POSIX when the path is a symlink.
//     Translated to errSymlinkRefused so checkOne can render a clean
//     reason without echoing the attacker-controlled symlink target.
//   - A non-regular file (FIFO, device, directory) is rejected after
//     stat-via-fd to defeat racy swaps.
//   - LimitReader bounds memory even if the file grew between the
//     Stat() check and the Read() call.
//   - On the rejection paths the file's *contents* are never read, so
//     symlink targets cannot leak into the audit trail.
func readBoundedNoFollow(path string, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// Linux + macOS both surface ELOOP for an O_NOFOLLOW symlink
		// open. Translate so checkOne does not have to know about
		// platform errnos.
		if errors.Is(err, syscall.ELOOP) {
			return nil, errSymlinkRefused
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	if info.Size() > maxBytes {
		return nil, errFileTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errFileTooLarge
	}
	return data, nil
}

// truncateForDisplay caps an attacker-influenced string before it lands
// in audit.log / judgment_reason. fmt's %q already escapes non-printable
// bytes, but it does not bound length — without truncation a 100KB
// .exit_code parse failure would echo the entire blob through to the
// Web UI dashboard.
func truncateForDisplay(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

func (w *Watcher) recordAudit(action string, id int64, value string) {
	w.auditor.Record(config.AuditEvent{
		Action: action,
		Key:    "task:" + strconv.FormatInt(id, 10),
		Value:  value,
	})
}
