package cli

import (
	"context"

	"github.com/haruotsu/marunage/internal/store"
)

// Mirror is the upstream-source synchronisation surface every CLI mutating
// command (`done`, `fail`, `rm`, `promote`, `reopen`) calls so that source
// plugins (markdown checkbox files, GitHub issues, Slack threads, ...) can
// reflect the change back to the place the task originated.
//
// PR-21 establishes the interface and a noop implementation; PR-50 and
// later wire concrete plugins (e.g. the markdown source's Complete /
// Delete entry points). Keeping the IF here in `internal/cli` rather than
// in `internal/source` avoids an import cycle: the CLI consumes Mirror,
// the source plugins implement it, and the wiring package (eventually
// `internal/source/composite` or similar) is built on top.
//
// Hook semantics:
//
//   - OnDone fires after a successful manual `done` or `fail` transition.
//     The task argument is the post-transition row (Status reflects the
//     new value) so a plugin can encode "completed at" in the upstream.
//   - OnDelete fires after a successful `rm`. The task argument is the
//     pre-delete snapshot so the plugin still has external_id available.
//   - OnReopen fires after a successful `reopen` (done/failed -> pending)
//     or `promote` (skipped -> pending). The task argument is the post-
//     transition row.
//
// All hooks return an error so a plugin can refuse to mutate the upstream
// (e.g. the markdown file is read-only). The CLI surfaces the error but
// does NOT roll back the SQLite mutation: the local store is the source
// of truth for marunage's state machine; mirror failure is a notification
// problem the operator addresses out of band. PR-50 may revisit this.
type Mirror interface {
	OnDone(ctx context.Context, t store.Task) error
	OnDelete(ctx context.Context, t store.Task) error
	OnReopen(ctx context.Context, t store.Task) error
}

// noopMirror satisfies Mirror without doing anything. PR-21 ships this as
// the production default so no upstream side effects happen until PR-50
// wires real plugins.
type noopMirror struct{}

func (noopMirror) OnDone(_ context.Context, _ store.Task) error   { return nil }
func (noopMirror) OnDelete(_ context.Context, _ store.Task) error { return nil }
func (noopMirror) OnReopen(_ context.Context, _ store.Task) error { return nil }

// mirrorFactory opens a Mirror given the resolved configPath. The factory
// shape mirrors taskRepoFactory so tests can install a fake via
// withMirrorFactory and production code falls through to
// productionMirrorFactory when no hook is set.
type mirrorFactory func(ctx context.Context, configPath string) (Mirror, error)

// mirrorFactoryHook is the package-private slot tests use via
// withMirrorFactory. Production callers see nil and fall through.
var mirrorFactoryHook mirrorFactory

// withMirrorFactory swaps in a fake factory and restores the prior hook
// on test completion, mirroring withTaskRepoFactory.
func withMirrorFactory(t interface{ Cleanup(func()) }, f mirrorFactory) {
	prev := mirrorFactoryHook
	mirrorFactoryHook = f
	t.Cleanup(func() { mirrorFactoryHook = prev })
}

// activeMirrorFactory returns the test override when one is installed,
// otherwise the production implementation.
func activeMirrorFactory() mirrorFactory {
	if mirrorFactoryHook != nil {
		return mirrorFactoryHook
	}
	return productionMirrorFactory
}

// productionMirrorFactory returns the noopMirror until PR-50 lands real
// source plugins. The configPath argument is reserved for that future
// wiring; until then the factory is a constant.
func productionMirrorFactory(_ context.Context, _ string) (Mirror, error) {
	return noopMirror{}, nil
}
