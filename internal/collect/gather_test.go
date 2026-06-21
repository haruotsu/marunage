package collect_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/collect"
	"github.com/haruotsu/marunage/internal/source"
	"github.com/haruotsu/marunage/internal/store"
)

// fakePlugin is a List-only source. It records nothing; Gather only
// calls List on it because it does not satisfy source.Sincer.
type fakePlugin struct {
	name    string
	tasks   []source.Task
	listErr error
}

func (p *fakePlugin) Name() string                                     { return p.name }
func (p *fakePlugin) List(context.Context) ([]source.Task, error)      { return p.tasks, p.listErr }
func (p *fakePlugin) Setup(context.Context, source.SetupOptions) error { return nil }
func (p *fakePlugin) AuthStatus(context.Context) (source.AuthStatus, error) {
	return source.AuthAuthenticated, nil
}

// fakeSincer adds the optional Since capability so Gather prefers it
// over List and threads the checkpoint through.
type fakeSincer struct {
	fakePlugin
	sinceCheckpoint string
	sinceCalled     bool
	sinceTasks      []source.Task
	sinceErr        error
}

func (p *fakeSincer) Since(_ context.Context, checkpoint string) ([]source.Task, error) {
	p.sinceCalled = true
	p.sinceCheckpoint = checkpoint
	return p.sinceTasks, p.sinceErr
}

// fakeCheckpoint is the in-memory kv_state stand-in implementing
// collect.Checkpoint.
type fakeCheckpoint struct {
	data   map[string]string
	sets   []kv
	getErr error
	setErr error
}

type kv struct{ key, value string }

func newFakeCheckpoint() *fakeCheckpoint { return &fakeCheckpoint{data: map[string]string{}} }

func (c *fakeCheckpoint) Get(_ context.Context, key string) (string, error) {
	if c.getErr != nil {
		return "", c.getErr
	}
	v, ok := c.data[key]
	if !ok {
		return "", store.ErrKVNotFound
	}
	return v, nil
}

func (c *fakeCheckpoint) Set(_ context.Context, key, value string) error {
	if c.setErr != nil {
		return c.setErr
	}
	c.sets = append(c.sets, kv{key, value})
	if c.data == nil {
		c.data = map[string]string{}
	}
	c.data[key] = value
	return nil
}

func TestGatherListOnlyNormalisesFields(t *testing.T) {
	p := &fakePlugin{
		name: "markdown",
		tasks: []source.Task{{
			Source:      "ignored-by-plugin",
			ExternalID:  "abc",
			Title:       "  do the thing  ",
			Body:        "details",
			Notes:       "note",
			Priority:    "high",
			SourcePath:  "/tmp/todo.md",
			Done:        true,
			RawMetadata: map[string]any{"line_number": 3},
		}},
	}
	got, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates; want 1", len(got))
	}
	c := got[0]
	if c.Source != "markdown" {
		t.Errorf("Source = %q; want forced to plugin name %q", c.Source, "markdown")
	}
	if c.ExternalID != "abc" {
		t.Errorf("ExternalID = %q; want abc", c.ExternalID)
	}
	if c.Title != "do the thing" {
		t.Errorf("Title = %q; want trimmed %q", c.Title, "do the thing")
	}
	if c.Body != "details" || c.Notes != "note" || c.Priority != "high" {
		t.Errorf("body/notes/priority mismatch: %+v", c)
	}
	if c.SourcePath != "/tmp/todo.md" {
		t.Errorf("SourcePath = %q", c.SourcePath)
	}
	if !c.Done {
		t.Errorf("Done = false; want true")
	}
	if c.RawMetadata["line_number"] != 3 {
		t.Errorf("RawMetadata not carried: %+v", c.RawMetadata)
	}
	if c.Verdict != "" {
		t.Errorf("Verdict = %q; want empty (undecided) for an ordinary candidate", c.Verdict)
	}
}

func TestGatherSincerReadsCheckpointAndCallsSince(t *testing.T) {
	cp := newFakeCheckpoint()
	cp.data[collect.CheckpointKeyPrefix+"gmail"] = "2026-06-01T00:00:00Z"
	p := &fakeSincer{
		fakePlugin: fakePlugin{name: "gmail"},
		sinceTasks: []source.Task{{ExternalID: "m1", Title: "hi"}},
	}
	_, err := collect.Gather(context.Background(), []source.Plugin{p}, cp)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !p.sinceCalled {
		t.Fatalf("Since was not called; Gather must prefer Sincer over List")
	}
	if p.sinceCheckpoint != "2026-06-01T00:00:00Z" {
		t.Errorf("Since checkpoint = %q; want the stored value", p.sinceCheckpoint)
	}
}

func TestGatherSincerFirstRunPassesEmptyCheckpoint(t *testing.T) {
	p := &fakeSincer{fakePlugin: fakePlugin{name: "gmail"}}
	_, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if !p.sinceCalled {
		t.Fatalf("Since not called")
	}
	if p.sinceCheckpoint != "" {
		t.Errorf("first-run checkpoint = %q; want empty (ErrKVNotFound treated as no checkpoint)", p.sinceCheckpoint)
	}
}

func TestGatherAdvancesCheckpointAfterSuccess(t *testing.T) {
	cp := newFakeCheckpoint()
	fixed := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	p := &fakeSincer{
		fakePlugin: fakePlugin{name: "gmail"},
		sinceTasks: []source.Task{{ExternalID: "m1", Title: "hi"}},
	}
	_, err := collect.Gather(context.Background(), []source.Plugin{p}, cp,
		collect.WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := collect.CheckpointKeyPrefix + "gmail"
	got, ok := cp.data[want]
	if !ok {
		t.Fatalf("checkpoint %q not advanced; sets=%+v", want, cp.sets)
	}
	if got != fixed.Format(time.RFC3339Nano) {
		t.Errorf("checkpoint value = %q; want %q", got, fixed.Format(time.RFC3339Nano))
	}
}

func TestGatherSourceFailureIsIsolated(t *testing.T) {
	bad := &fakePlugin{name: "slack", listErr: errors.New("api down")}
	good := &fakePlugin{name: "markdown", tasks: []source.Task{{ExternalID: "x", Title: "ok"}}}
	got, err := collect.Gather(context.Background(), []source.Plugin{bad, good}, newFakeCheckpoint())
	if err == nil {
		t.Fatalf("Gather err = nil; want the slack failure surfaced")
	}
	if !strings.Contains(err.Error(), "slack") || !strings.Contains(err.Error(), "api down") {
		t.Errorf("err = %v; want it to name the failing source and cause", err)
	}
	if len(got) != 1 || got[0].ExternalID != "x" {
		t.Errorf("got %+v; want the healthy source's candidate to survive", got)
	}
}

func TestGatherDropsGmailPromotions(t *testing.T) {
	p := &fakePlugin{name: "gmail", tasks: []source.Task{{
		ExternalID:  "promo1",
		Title:       "50% OFF today only",
		RawMetadata: map[string]any{"labels": []string{"CATEGORY_PROMOTIONS"}},
	}}}
	got, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates; want the dropped one retained for No-silent-loss", len(got))
	}
	if got[0].Verdict != collect.VerdictDrop {
		t.Errorf("Verdict = %q; want drop for a gmail promotion", got[0].Verdict)
	}
	if got[0].Reason == "" {
		t.Errorf("dropped candidate must carry a reason for the audit trail")
	}
}

func TestGatherDropsGithubNotificationEmail(t *testing.T) {
	p := &fakePlugin{name: "gmail", tasks: []source.Task{{
		ExternalID:  "n1",
		Title:       "[repo] Re: some issue",
		RawMetadata: map[string]any{"from": "notifications@github.com"},
	}}}
	got, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got[0].Verdict != collect.VerdictDrop {
		t.Errorf("Verdict = %q; want drop for a github notification email", got[0].Verdict)
	}
}

// A genuine task from gmail (no ad label, human sender) must survive
// early triage undecided so the manage layer gets to judge it.
func TestGatherKeepsOrdinaryGmail(t *testing.T) {
	p := &fakePlugin{name: "gmail", tasks: []source.Task{{
		ExternalID:  "real1",
		Title:       "Can you review my PR?",
		RawMetadata: map[string]any{"from": "teammate@example.com", "labels": []string{"INBOX"}},
	}}}
	got, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint())
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got[0].Verdict != "" {
		t.Errorf("Verdict = %q; want empty for an ordinary gmail task", got[0].Verdict)
	}
}

func TestGatherWithRulesOverridesDefaults(t *testing.T) {
	dropAll := collect.Rule{
		Name:    "drop-everything",
		Verdict: collect.VerdictDrop,
		Reason:  "test rule",
		Match:   func(collect.Candidate) bool { return true },
	}
	p := &fakePlugin{name: "markdown", tasks: []source.Task{{ExternalID: "x", Title: "anything"}}}
	got, err := collect.Gather(context.Background(), []source.Plugin{p}, newFakeCheckpoint(),
		collect.WithRules([]collect.Rule{dropAll}))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if got[0].Verdict != collect.VerdictDrop {
		t.Errorf("custom rule not applied; Verdict = %q", got[0].Verdict)
	}
}
