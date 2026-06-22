package loop_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/loop"
	"github.com/haruotsu/marunage/internal/manage"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// An empty-title candidate is escalated to needs-human by the rule engine. It
// must still persist (with a placeholder title) so it surfaces in the
// waiting_human queue for a human to triage — otherwise the store's
// title-required constraint drops the row and the escalation is silently lost
// (the human never sees what manage decided to hand them).
func TestRunOnce_ManagePipeline_EmptyTitleEscalatesToWaitingHuman(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "blank1", Title: "", Body: "a body but no title"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row := rowByExternalID(t, f, "blank1")
	if row.Status != store.StatusWaitingHuman {
		t.Errorf("Status = %q; want waiting_human (escalated, not dropped)", row.Status)
	}
	if strings.TrimSpace(row.Title) == "" {
		t.Errorf("Title empty; want a placeholder so the row is visible to a human")
	}
	if row.PlanLabel != string(collect.VerdictNeedsHuman) {
		t.Errorf("PlanLabel = %q; want needs-human", row.PlanLabel)
	}
}

// rowByExternalID lists the tasks table and returns the row with the given
// external_id, failing the test when absent.
func rowByExternalID(t *testing.T, f *fixture, extID string) store.Task {
	t.Helper()
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.ExternalID == extID {
			return r
		}
	}
	t.Fatalf("no row with external_id %q", extID)
	return store.Task{}
}

func registerListPlugin(t *testing.T, f *fixture, name string, tasks []source.Task) {
	t.Helper()
	plug := &fakePlugin{
		name:   name,
		listFn: func(context.Context) ([]source.Task, error) { return tasks, nil },
	}
	if err := f.reg.Register(plug); err != nil {
		t.Fatalf("register: %v", err)
	}
}

// A candidate with a title and body clears the rule engine, lands pending with
// plan_label=ready + a rank, and shows up in the dispatchable set.
func TestRunOnce_ManagePipeline_ReadyCandidateIsDispatchable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "r1", Title: "Do it", Body: "details"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row := rowByExternalID(t, f, "r1")
	if row.Status != store.StatusPending {
		t.Errorf("Status = %q; want pending", row.Status)
	}
	if row.PlanLabel != "ready" {
		t.Errorf("PlanLabel = %q; want ready", row.PlanLabel)
	}
	if row.PlanRank != 1 {
		t.Errorf("PlanRank = %d; want 1", row.PlanRank)
	}
	if row.PlanReason == "" {
		t.Errorf("PlanReason empty; want a rationale recorded")
	}

	disp, err := f.repo.List(f.ctx, store.ListFilter{Statuses: []string{store.StatusPending}, DispatchableOnly: true})
	if err != nil {
		t.Fatalf("List dispatchable: %v", err)
	}
	if len(disp) != 1 || disp[0].ExternalID != "r1" {
		t.Errorf("dispatchable set = %+v; want only r1", disp)
	}
	// Dispatch + render still ran exactly once.
	if len(f.disp.snapshot()) != 1 {
		t.Errorf("dispatcher ran %d times; want 1", len(f.disp.snapshot()))
	}
	if f.rend.count() != 1 {
		t.Errorf("render ran %d times; want 1", f.rend.count())
	}
}

// A title-only candidate (no body) is actionable — the title is enough — so it
// becomes ready/pending and IS dispatchable, NOT escalated to a human.
func TestRunOnce_ManagePipeline_TitleOnlyIsReady(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "e1", Title: "review the deploy doc"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row := rowByExternalID(t, f, "e1")
	if row.Status != store.StatusPending {
		t.Errorf("Status = %q; want pending", row.Status)
	}
	if row.PlanLabel != string(collect.VerdictReady) {
		t.Errorf("PlanLabel = %q; want ready", row.PlanLabel)
	}
	disp, err := f.repo.List(f.ctx, store.ListFilter{Statuses: []string{store.StatusPending}, DispatchableOnly: true})
	if err != nil {
		t.Fatalf("List dispatchable: %v", err)
	}
	if len(disp) != 1 {
		t.Errorf("dispatchable set = %+v; want the title-only ready row", disp)
	}
}

// A cwd-violating candidate drops: status skipped, plan_label drop. No silent
// loss — the row is still persisted for `marunage review`.
func TestRunOnce_ManagePipeline_CwdViolationDrops(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "d1", Title: "bad cwd", Body: "x", Notes: `{"cwd":"/etc"}`},
	})
	l := f.newLoop(t,
		loop.WithManageStore(f.repo),
		loop.WithManageOptions(manage.WithAllowedCwdPrefixes([]string{"/home/me/works"})),
	)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	row := rowByExternalID(t, f, "d1")
	if row.Status != store.StatusSkipped {
		t.Errorf("Status = %q; want skipped", row.Status)
	}
	if row.PlanLabel != "drop" {
		t.Errorf("PlanLabel = %q; want drop", row.PlanLabel)
	}
}

// An upstream-complete candidate keeps status done regardless of the verdict,
// so a finished item is never re-queued.
func TestRunOnce_ManagePipeline_DonePreservesDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "done1", Title: "finished", Body: "x", Done: true},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	row := rowByExternalID(t, f, "done1")
	if row.Status != store.StatusDone {
		t.Errorf("Status = %q; want done", row.Status)
	}
	// A finished item carries no plan label: the management verdict is moot
	// for something already complete upstream.
	if row.PlanLabel != "" {
		t.Errorf("PlanLabel = %q; want empty for an upstream-done item", row.PlanLabel)
	}
}

// Every decision is persisted (invariant #1 No silent loss): a ready row and a
// dropped row both land, even though only the ready one is dispatchable.
func TestRunOnce_ManagePipeline_PersistsAllDecisions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "ready1", Title: "keep", Body: "x"},
		{Source: "manual", ExternalID: "drop1", Title: "toss", Body: "x", Notes: `{"cwd":"/etc"}`},
	})
	l := f.newLoop(t,
		loop.WithManageStore(f.repo),
		loop.WithManageOptions(manage.WithAllowedCwdPrefixes([]string{"/home/me/works"})),
	)
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; want 2 (both ready and drop persisted)", len(rows))
	}
}

// A needs-human row that gains enough info on a later tick promotes cleanly to
// ready+pending. Status and plan_label must move together: leaving status at
// waiting_human while plan_label flips to ready would create a row the
// dispatcher can never pick (status != pending) yet is labelled ready.
func TestRunOnce_ManagePipeline_PromotesWaitingHumanToReady(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// Seed a planner-owned waiting_human row directly (as an earlier
	// escalation would leave it). The info-insufficient rule no longer
	// produces needs-human from a title-only candidate, so we set the state
	// rather than driving it through a tick.
	id, err := f.repo.Insert(f.ctx, store.Task{Source: "manual", ExternalID: "x", Title: "need info", Body: "fragment"})
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := f.repo.UpdateStatus(f.ctx, id, store.StatusWaitingHuman); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if err := f.repo.SetPlan(f.ctx, id, string(collect.VerdictNeedsHuman), "seed", 0, 0, f.now); err != nil {
		t.Fatalf("seed plan: %v", err)
	}

	// Re-emitting the row as a ready candidate must move status AND plan_label
	// together: leaving status at waiting_human while plan_label flips to ready
	// would create a row the dispatcher can never pick yet is labelled ready.
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "x", Title: "need info", Body: "now has detail"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	row := rowByExternalID(t, f, "x")
	if row.Status != store.StatusPending {
		t.Errorf("status = %q; want pending (promoted)", row.Status)
	}
	if row.PlanLabel != "ready" {
		t.Errorf("plan_label = %q; want ready", row.PlanLabel)
	}
}

// A row the executor is already running must not be reset by a re-emitted
// candidate: re-classification leaves executor-owned states (running / done /
// failed) untouched.
func TestRunOnce_ManagePipeline_DoesNotResetRunningRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "r", Title: "T", Body: "b"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	row := rowByExternalID(t, f, "r")
	// Simulate the dispatcher claiming the ready row.
	if err := f.repo.UpdateStatus(f.ctx, row.ID, store.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus running: %v", err)
	}
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if got := rowByExternalID(t, f, "r").Status; got != store.StatusRunning {
		t.Errorf("status = %q; want running (re-eval must not reset an in-flight row)", got)
	}
}

// setPlanFailStore wraps the real repo but fails SetPlan, so the test can drive
// persistDecision's audit-and-continue error path without a flaky real failure.
type setPlanFailStore struct {
	*store.TaskRepo
	err error
}

func (s setPlanFailStore) SetPlan(context.Context, int64, string, string, float64, int, time.Time) error {
	return s.err
}

// A per-row persist failure is audited and does not abort the tick — the row is
// still inserted (Insert precedes SetPlan) and RunOnce returns nil.
func TestRunOnce_ManagePipeline_PersistErrorIsAuditedAndContinues(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "a", Title: "T", Body: "b"},
	})
	fault := setPlanFailStore{TaskRepo: f.repo, err: errors.New("disk full")}
	l := f.newLoop(t, loop.WithManageStore(fault))

	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce should not bubble a per-row persist failure; got %v", err)
	}
	if _, err := f.repo.GetBySourceExternalID(f.ctx, "manual", "a"); err != nil {
		t.Errorf("row should be inserted despite SetPlan failure: %v", err)
	}
	var sawFail bool
	for _, e := range f.aud.snapshot() {
		if e.Action == "loop.manage.fail" {
			sawFail = true
			if e.Value == "" {
				t.Errorf("loop.manage.fail audit value empty; want the redacted error")
			}
		}
	}
	if !sawFail {
		t.Errorf("expected loop.manage.fail audit; got %v", f.aud.actions())
	}
}

// A re-emitted candidate is idempotent: the second tick does not create a
// duplicate row and the plan columns stay set.
func TestRunOnce_ManagePipeline_IdempotentAcrossTicks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "r1", Title: "Do it", Body: "details"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	rows, err := f.repo.List(f.ctx, store.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1 (idempotent re-discovery)", len(rows))
	}
	if rows[0].PlanLabel == "" {
		t.Errorf("PlanLabel empty after re-tick; plan columns should stay set")
	}
}
