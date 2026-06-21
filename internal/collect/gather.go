// Package collect is marunage's collection layer (redesign §2). It binds
// the Source plugins together behind a single entry point, Gather, which
// fetches raw upstream items, normalises them into Candidate values, and
// runs a cheap rule-based early triage that drops obvious noise before
// the downstream manage layer spends any LLM budget.
//
// collect is the most upstream package and therefore owns the vocabulary
// the lower layers share: Candidate, Verdict, and the early-triage
// Decision/Apply hook (redesign §3.4 "型は最上流が源泉"). It holds no state
// of its own — checkpoints live in marunage's single tasks.db and are
// reached only through the injected Checkpoint interface (redesign D3).
package collect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// CheckpointKeyPrefix namespaces the per-source discovery checkpoints in
// kv_state. It deliberately matches the prefix the legacy loop package
// uses ("loop.checkpoint.") so that when cmd is rewired onto the
// collect→manage→exec pipeline (PR-R05) the existing per-source
// checkpoints carry over untouched and Discovery behaviour stays
// unchanged.
const CheckpointKeyPrefix = "loop.checkpoint."

// Compile-time proof that the production repos satisfy collect's narrow
// interfaces, so a method-signature change in store fails to build here
// rather than at the cmd wiring seam (PR-R05).
var (
	_ Checkpoint = (*store.KVStateRepo)(nil)
	_ Store      = (*store.TaskRepo)(nil)
)

// Checkpoint is the narrow read/write surface Gather needs against the
// kv_state table. *store.KVStateRepo satisfies it implicitly, so cmd
// hands the concrete repo in while tests inject a fake. collect itself
// stays stateless — it only reads the last checkpoint to drive a
// Sincer's incremental fetch and writes the new one on success.
type Checkpoint interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
}

// Option configures Gather. The zero set of options yields production
// defaults: the real clock and DefaultRules.
type Option func(*gatherer)

// WithClock injects the clock used to stamp advanced checkpoints, so
// tests can assert an exact value without sleeping. Production leaves it
// unset and gets time.Now.
func WithClock(now func() time.Time) Option {
	return func(g *gatherer) {
		if now != nil {
			g.now = now
		}
	}
}

// WithRules replaces the default early-triage rule set. Passing nil or an
// empty slice disables early triage entirely (every candidate stays
// undecided). Primarily a test seam today; PR-R06 may drive the rules
// from config.
func WithRules(rules []Rule) Option {
	return func(g *gatherer) { g.rules = rules }
}

type gatherer struct {
	now   func() time.Time
	rules []Rule
}

// Gather fetches from every source, normalises the results into
// Candidates, and applies early triage. A single source's failure does
// not abort the rest: its error is collected (errors.Join) and returned
// alongside the candidates the healthy sources produced, preserving the
// "one bad plugin must not blind the whole tick" behaviour Discovery has
// today.
//
// Dropped candidates are NOT filtered out — they are returned carrying
// VerdictDrop and a Reason so the caller can persist them as skipped
// rows (invariant #1 No silent loss). The caller forwards only the
// undecided candidates to the manage layer.
//
// cp is the checkpoint gateway: for sources that implement
// source.Sincer, Gather reads the last checkpoint and passes it to Since,
// then advances the checkpoint after a successful fetch. List-only
// sources ignore the checkpoint entirely.
func Gather(ctx context.Context, sources []source.Plugin, cp Checkpoint, opts ...Option) ([]Candidate, error) {
	g := &gatherer{now: time.Now, rules: DefaultRules()}
	for _, opt := range opts {
		opt(g)
	}

	var (
		out  []Candidate
		errs []error
	)
	for _, p := range sources {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		name := p.Name()
		tasks, err := g.fetch(ctx, p, cp)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %s: %w", name, err))
			continue
		}
		for _, t := range tasks {
			c := normalise(t, name)
			c.Verdict, c.Reason = classify(c, g.rules)
			out = append(out, c)
		}
		if err := g.advanceCheckpoint(ctx, cp, name); err != nil {
			errs = append(errs, fmt.Errorf("source %s: advance checkpoint: %w", name, err))
		}
	}
	return out, errors.Join(errs...)
}

// fetch prefers a source's incremental Since over List when the plugin
// implements source.Sincer, mirroring the loop package's contract. The
// checkpoint is read from kv_state; a missing checkpoint (first run) is
// the documented ErrKVNotFound and becomes an empty checkpoint rather
// than a fatal error.
func (g *gatherer) fetch(ctx context.Context, p source.Plugin, cp Checkpoint) ([]source.Task, error) {
	s, ok := p.(source.Sincer)
	if !ok {
		return p.List(ctx)
	}
	checkpoint := ""
	if cp != nil {
		v, err := cp.Get(ctx, CheckpointKeyPrefix+p.Name())
		switch {
		case err == nil:
			checkpoint = v
		case errors.Is(err, store.ErrKVNotFound):
			// First run for this source: no checkpoint yet.
		default:
			return nil, fmt.Errorf("read checkpoint: %w", err)
		}
	}
	return s.Since(ctx, checkpoint)
}

// advanceCheckpoint stamps the per-source checkpoint with the current
// clock as RFC3339Nano, matching the loop package's format so the value
// a Sincer reads back next run is the wall-clock time of the last
// successful fetch. A nil checkpoint store (a caller that opted out)
// makes this a no-op.
func (g *gatherer) advanceCheckpoint(ctx context.Context, cp Checkpoint, name string) error {
	if cp == nil {
		return nil
	}
	value := g.now().UTC().Format(time.RFC3339Nano)
	if err := cp.Set(ctx, CheckpointKeyPrefix+name, value); err != nil {
		return err
	}
	return nil
}
