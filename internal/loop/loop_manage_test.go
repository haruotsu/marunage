package loop_test

import (
	"context"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/loop"
	"github.com/haruotsu/marunage/internal/manage"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

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

// An empty-body candidate escalates: status waiting_human, plan_label
// needs-human, and it is NOT dispatchable.
func TestRunOnce_ManagePipeline_EmptyBodyEscalates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	registerListPlugin(t, f, "manual", []source.Task{
		{Source: "manual", ExternalID: "e1", Title: "no body"},
	})
	l := f.newLoop(t, loop.WithManageStore(f.repo))
	if err := l.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	row := rowByExternalID(t, f, "e1")
	if row.Status != store.StatusWaitingHuman {
		t.Errorf("Status = %q; want waiting_human", row.Status)
	}
	if row.PlanLabel != string(collect.VerdictNeedsHuman) {
		t.Errorf("PlanLabel = %q; want needs-human", row.PlanLabel)
	}
	disp, err := f.repo.List(f.ctx, store.ListFilter{Statuses: []string{store.StatusPending}, DispatchableOnly: true})
	if err != nil {
		t.Fatalf("List dispatchable: %v", err)
	}
	if len(disp) != 0 {
		t.Errorf("dispatchable set = %+v; want empty (needs-human is not dispatchable)", disp)
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
