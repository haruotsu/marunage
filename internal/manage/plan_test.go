package manage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/store"
)

// fakeStore is the read-only test double for the management layer's Store
// surface. It filters the seeded rows the same way *store.TaskRepo.List
// does for the Statuses / Sources constraints Plan uses.
type fakeStore struct {
	tasks   []store.Task
	listErr error
	getErr  error
}

func (f *fakeStore) List(_ context.Context, fil store.ListFilter) ([]store.Task, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []store.Task
	for _, t := range f.tasks {
		if len(fil.Statuses) > 0 && !inSlice(fil.Statuses, t.Status) {
			continue
		}
		if len(fil.Sources) > 0 && !inSlice(fil.Sources, t.Source) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, id int64) (store.Task, error) {
	if f.getErr != nil {
		return store.Task{}, f.getErr
	}
	for _, t := range f.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return store.Task{}, store.ErrNotFound
}

func inSlice(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// decisionFor returns the planned decision for the candidate with the given
// title, failing the test when absent.
func decisionFor(t *testing.T, plan ReadyPlan, title string) PlannedCandidate {
	t.Helper()
	for _, d := range plan.Decisions {
		if d.Candidate.Title == title {
			return d
		}
	}
	t.Fatalf("no decision for candidate %q", title)
	return PlannedCandidate{}
}

func TestPlanRuleDepsIncompleteHolds(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{{ID: 7, Status: store.StatusPending}}}
	cands := []collect.Candidate{
		{Title: "needs #7", Body: "do it", Notes: `{"depends_on":[7]}`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "needs #7").Verdict; got != collect.VerdictHold {
		t.Errorf("verdict = %q; want hold", got)
	}
}

func TestPlanRuleDepsCompleteClears(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{{ID: 7, Status: store.StatusDone}}}
	cands := []collect.Candidate{
		{Title: "needs #7", Body: "do it", Notes: `{"depends_on":[7]}`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "needs #7").Verdict; got != collect.VerdictReady {
		t.Errorf("verdict = %q; want ready", got)
	}
}

func TestPlanRuleMissingDepHolds(t *testing.T) {
	st := &fakeStore{} // dep 99 does not exist
	cands := []collect.Candidate{
		{Title: "needs #99", Body: "do it", Notes: `{"depends_on":[99]}`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "needs #99").Verdict; got != collect.VerdictHold {
		t.Errorf("verdict = %q; want hold (missing dep is not done)", got)
	}
}

func TestPlanRuleEmptyBodyEscalates(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{{Title: "no body", Body: ""}}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "no body").Verdict; got != collect.VerdictNeedsHuman {
		t.Errorf("verdict = %q; want needs-human", got)
	}
}

func TestPlanRuleEmptyTitleEscalates(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{{Title: "", Body: "has body"}}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Decisions[0].Verdict; got != collect.VerdictNeedsHuman {
		t.Errorf("verdict = %q; want needs-human", got)
	}
}

func TestPlanRuleCwdViolationDrops(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "bad cwd", Body: "x", Notes: `{"cwd":"/etc"}`},
	}
	plan, err := Plan(context.Background(), cands, st, WithAllowedCwdPrefixes([]string{"/home/me/works"}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "bad cwd").Verdict; got != collect.VerdictDrop {
		t.Errorf("verdict = %q; want drop", got)
	}
}

func TestPlanRuleCwdAllowedClears(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "ok cwd", Body: "x", Notes: `{"cwd":"/home/me/works/repo"}`},
	}
	plan, err := Plan(context.Background(), cands, st, WithAllowedCwdPrefixes([]string{"/home/me/works"}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "ok cwd").Verdict; got != collect.VerdictReady {
		t.Errorf("verdict = %q; want ready", got)
	}
}

func TestPlanRuleDuplicateDrops(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{
		{ID: 1, Source: "github", ExternalID: "42", Status: store.StatusDone},
	}}
	cands := []collect.Candidate{
		{Source: "github", ExternalID: "42", Title: "dup", Body: "x"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "dup").Verdict; got != collect.VerdictDrop {
		t.Errorf("verdict = %q; want drop", got)
	}
}

func TestPlanRuleDuplicateOnlyAgainstDoneOrSkipped(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{
		{ID: 1, Source: "github", ExternalID: "42", Status: store.StatusPending},
	}}
	cands := []collect.Candidate{
		{Source: "github", ExternalID: "42", Title: "not dup", Body: "x"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "not dup").Verdict; got != collect.VerdictReady {
		t.Errorf("verdict = %q; want ready (pending peer is not a duplicate)", got)
	}
}

// Precedence: a candidate that both duplicates a finished row and depends on
// an incomplete task must drop — the decisive "do not do at all" verdict wins
// over the recoverable hold.
func TestPlanRuleDropBeatsHold(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{
		{ID: 1, Source: "github", ExternalID: "42", Status: store.StatusSkipped},
		{ID: 7, Status: store.StatusPending},
	}}
	cands := []collect.Candidate{
		{Source: "github", ExternalID: "42", Title: "dup+dep", Body: "x", Notes: `{"depends_on":[7]}`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "dup+dep").Verdict; got != collect.VerdictDrop {
		t.Errorf("verdict = %q; want drop", got)
	}
}

func TestPlanRespectsEarlyTriageDrop(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "ad", Body: "buy now", Verdict: collect.VerdictDrop, Reason: "early triage: promo"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	d := decisionFor(t, plan, "ad")
	if d.Verdict != collect.VerdictDrop {
		t.Errorf("verdict = %q; want drop (early triage decision respected)", d.Verdict)
	}
	if d.Reason != "early triage: promo" {
		t.Errorf("reason = %q; want early-triage reason preserved", d.Reason)
	}
	if len(plan.Ready) != 0 {
		t.Errorf("Ready = %d; want 0", len(plan.Ready))
	}
}

// LLM-less Plan still produces an ordered ready列: higher priority first.
func TestPlanOrdersReadyByPriorityWithoutLLM(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "low", Body: "x", Priority: "1"},
		{Title: "high", Body: "x", Priority: "9"},
		{Title: "mid", Body: "x", Priority: "5"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 3 {
		t.Fatalf("Ready = %d; want 3", len(plan.Ready))
	}
	wantOrder := []string{"high", "mid", "low"}
	for i, w := range wantOrder {
		if plan.Ready[i].Candidate.Title != w {
			t.Errorf("Ready[%d] = %q; want %q", i, plan.Ready[i].Candidate.Title, w)
		}
		if plan.Ready[i].Rank != i+1 {
			t.Errorf("Ready[%d].Rank = %d; want %d", i, plan.Ready[i].Rank, i+1)
		}
	}
}

// Ranking is stable for equal priorities: input order is preserved.
func TestPlanStableOrderForEqualPriority(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "first", Body: "x", Priority: "5"},
		{Title: "second", Body: "x", Priority: "5"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "first" || plan.Ready[1].Candidate.Title != "second" {
		t.Errorf("order = [%q,%q]; want [first,second]",
			plan.Ready[0].Candidate.Title, plan.Ready[1].Candidate.Title)
	}
}

// Due-soon candidates outrank equal-priority peers (priority boost, verdict
// unchanged).
func TestPlanDueBoostRanksAhead(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	soon := now.Add(1 * time.Hour).Format(time.RFC3339)
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "no-due", Body: "x", Priority: "5"},
		{Title: "due-soon", Body: "x", Priority: "5", Notes: fmt.Sprintf(`{"due":%q}`, soon)},
	}
	plan, err := Plan(context.Background(), cands, st, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "due-soon" {
		t.Errorf("Ready[0] = %q; want due-soon", plan.Ready[0].Candidate.Title)
	}
	if got := decisionFor(t, plan, "due-soon").Verdict; got != collect.VerdictReady {
		t.Errorf("due-soon verdict = %q; want ready (boost keeps verdict)", got)
	}
}

// Far-future due dates do not boost.
func TestPlanDueFarDoesNotBoost(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	far := now.Add(30 * 24 * time.Hour).Format(time.RFC3339)
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "high", Body: "x", Priority: "5"},
		{Title: "due-far", Body: "x", Priority: "1", Notes: fmt.Sprintf(`{"due":%q}`, far)},
	}
	plan, err := Plan(context.Background(), cands, st, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "high" {
		t.Errorf("Ready[0] = %q; want high (far due gets no boost)", plan.Ready[0].Candidate.Title)
	}
}

// A lock-contended candidate stays ready but is serialized to the back.
func TestPlanLockConflictSerializesButStaysReady(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{
		{ID: 1, Status: store.StatusRunning, LockKey: "git-repo"},
	}}
	lockKeys := map[string]string{"^repo:.*": "git-repo"}
	cands := []collect.Candidate{
		{Title: "contended", Body: "x", Priority: "9", Notes: `{"lock_hint":"repo:marunage"}`},
		{Title: "free", Body: "x", Priority: "1"},
	}
	plan, err := Plan(context.Background(), cands, st, WithLockKeys(lockKeys))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "contended").Verdict; got != collect.VerdictReady {
		t.Errorf("contended verdict = %q; want ready", got)
	}
	if !decisionFor(t, plan, "contended").Serialized {
		t.Errorf("contended Serialized = false; want true")
	}
	// Despite higher priority, the contended row is ordered last.
	if plan.Ready[len(plan.Ready)-1].Candidate.Title != "contended" {
		t.Errorf("last ready = %q; want contended (serialized to back)",
			plan.Ready[len(plan.Ready)-1].Candidate.Title)
	}
}

func TestPlanSplitsReadyFromOther(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "ready1", Body: "x", Priority: "1"},
		{Title: "escalate", Body: ""}, // needs-human
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 1 || plan.Ready[0].Candidate.Title != "ready1" {
		t.Errorf("Ready = %+v; want only ready1", plan.Ready)
	}
	if len(plan.Decisions) != 2 {
		t.Errorf("Decisions = %d; want 2 (every candidate recorded)", len(plan.Decisions))
	}
}

// Toggling a rule off via config disables its cut-off.
func TestPlanRuleTogglesOff(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{{ID: 7, Status: store.StatusPending}}}
	cands := []collect.Candidate{
		{Title: "needs #7", Body: "do it", Notes: `{"depends_on":[7]}`},
	}
	rules := DefaultRules()
	rules.BlockIfDepsIncomplete = false
	plan, err := Plan(context.Background(), cands, st, WithRules(rules))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "needs #7").Verdict; got != collect.VerdictReady {
		t.Errorf("verdict = %q; want ready (deps rule disabled)", got)
	}
}

// A store List failure during the duplicate snapshot is catastrophic and
// surfaces to the caller rather than silently mislabelling candidates.
func TestPlanPropagatesStoreListError(t *testing.T) {
	boom := errors.New("db down")
	st := &fakeStore{listErr: boom}
	cands := []collect.Candidate{{Title: "x", Body: "y"}}
	_, err := Plan(context.Background(), cands, st)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want db down", err)
	}
}

// A non-NotFound store error during a dependency lookup is catastrophic and
// surfaces to the caller (symmetric to the List-error path).
func TestPlanPropagatesDependencyGetError(t *testing.T) {
	boom := errors.New("db down")
	st := &fakeStore{getErr: boom}
	cands := []collect.Candidate{
		{Title: "needs #7", Body: "do it", Notes: `{"depends_on":[7]}`},
	}
	_, err := Plan(context.Background(), cands, st)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want db down", err)
	}
}

// An overdue (past) deadline still boosts — it is the most urgent case.
func TestPlanOverdueDueBoostsRanksAhead(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour).Format(time.RFC3339)
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "no-due", Body: "x", Priority: "5"},
		{Title: "overdue", Body: "x", Priority: "5", Notes: fmt.Sprintf(`{"due":%q}`, past)},
	}
	plan, err := Plan(context.Background(), cands, st, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "overdue" {
		t.Errorf("Ready[0] = %q; want overdue (past due is most urgent)", plan.Ready[0].Candidate.Title)
	}
}

// An unparseable due date neither boosts nor crashes — best-effort parsing
// leaves the candidate ready at its base priority.
func TestPlanUnparseableDueDoesNotBoost(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "high", Body: "x", Priority: "5"},
		{Title: "bad-due", Body: "x", Priority: "1", Notes: `{"due":"not-a-date"}`},
	}
	plan, err := Plan(context.Background(), cands, st, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Ready[0].Candidate.Title != "high" {
		t.Errorf("Ready[0] = %q; want high (unparseable due gives no boost)", plan.Ready[0].Candidate.Title)
	}
	if got := decisionFor(t, plan, "bad-due").Verdict; got != collect.VerdictReady {
		t.Errorf("bad-due verdict = %q; want ready", got)
	}
}

// Malformed notes JSON is swallowed: the candidate is judged as if the notes
// carried no metadata rather than crashing or being mis-held.
func TestPlanMalformedNotesIsInert(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "broken notes", Body: "x", Notes: `{not valid json`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "broken notes").Verdict; got != collect.VerdictReady {
		t.Errorf("verdict = %q; want ready (malformed notes inert)", got)
	}
}

// With multiple dependencies, any single incomplete one holds the candidate.
func TestPlanMultipleDepsAnyIncompleteHolds(t *testing.T) {
	st := &fakeStore{tasks: []store.Task{
		{ID: 7, Status: store.StatusDone},
		{ID: 8, Status: store.StatusPending},
	}}
	cands := []collect.Candidate{
		{Title: "needs #7 and #8", Body: "do it", Notes: `{"depends_on":[7,8]}`},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := decisionFor(t, plan, "needs #7 and #8").Verdict; got != collect.VerdictHold {
		t.Errorf("verdict = %q; want hold (one dep still pending)", got)
	}
}

func TestPlanEmptyCandidateList(t *testing.T) {
	plan, err := Plan(context.Background(), nil, &fakeStore{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 0 || len(plan.Decisions) != 0 {
		t.Errorf("Ready=%d Decisions=%d; want 0/0", len(plan.Ready), len(plan.Decisions))
	}
}

// An unknown verdict produced for a candidate routes to Other (not ready),
// via the registry's safe needs-human fallback (原則2).
func TestPlanUnknownVerdictNotDispatchable(t *testing.T) {
	st := &fakeStore{}
	cands := []collect.Candidate{
		{Title: "weird", Body: "x", Verdict: collect.Verdict("delegate"), Reason: "custom"},
	}
	plan, err := Plan(context.Background(), cands, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Ready) != 0 {
		t.Errorf("Ready = %d; want 0 (unknown verdict is not dispatchable)", len(plan.Ready))
	}
	if got := decisionFor(t, plan, "weird").Verdict; got != collect.Verdict("delegate") {
		t.Errorf("verdict = %q; want delegate preserved", got)
	}
}
