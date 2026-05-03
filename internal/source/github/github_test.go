package github

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// runCall captures one invocation against the fake runner. Tests assert on
// the captured slice rather than the runner's behaviour, so the production
// Plugin code never has to expose its argv-building helpers.
type runCall struct {
	Name string
	Args []string
}

// fakeRunner returns canned stdout/stderr/err per invocation. The matching
// is order-sensitive: each call pops the next response, mirroring how a
// real `gh` invocation produces a fresh exit each time. Tests register the
// expected sequence; an unexpected extra call surfaces as t.Fatalf.
type fakeRunner struct {
	mu        sync.Mutex
	calls     []runCall
	responses []runResponse
}

type runResponse struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, runCall{Name: name, Args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return nil, nil, errors.New("fakeRunner: unexpected call (no canned response)")
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return []byte(r.stdout), []byte(r.stderr), r.err
}

// issueJSON / prJSON are the canned `gh search issues --json ...` shapes
// the parser must accept. Field names mirror gh's own JSON schema so a real
// integration test could swap the strings for live output without changing
// the assertions below.
const issueJSON = `[
  {
    "number": 12,
    "title": "issue title",
    "body": "issue body",
    "updatedAt": "2026-01-02T03:04:05Z",
    "url": "https://github.com/owner/repo/issues/12",
    "repository": {"nameWithOwner": "owner/repo"}
  }
]`

const prJSON = `[
  {
    "number": 34,
    "title": "pr title",
    "body": "pr body",
    "updatedAt": "2026-02-03T04:05:06Z",
    "url": "https://github.com/owner/repo/pull/34",
    "repository": {"nameWithOwner": "owner/repo"}
  }
]`

func TestListInvokesGhSearchForIssuesAndPRs(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: issueJSON},
		{stdout: prJSON},
	}}
	p := New(WithRunner(fr))

	tasks, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("len = %d, %+v", len(tasks), tasks)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(fr.calls), fr.calls)
	}
	for i, c := range fr.calls {
		if c.Name != "gh" {
			t.Errorf("calls[%d].Name = %q, want gh", i, c.Name)
		}
		if len(c.Args) < 3 || c.Args[0] != "search" {
			t.Errorf("calls[%d].Args = %v, want gh search ...", i, c.Args)
		}
	}
	if got := fr.calls[0].Args[1]; got != "issues" {
		t.Errorf("first call should be `gh search issues`, got %q", got)
	}
	if got := fr.calls[1].Args[1]; got != "prs" {
		t.Errorf("second call should be `gh search prs`, got %q", got)
	}
	for i, c := range fr.calls {
		// The query (`is:open assignee:@me`) is the third positional arg.
		if c.Args[2] != "is:open assignee:@me" {
			t.Errorf("calls[%d] query = %q, want is:open assignee:@me", i, c.Args[2])
		}
		// `--json` request must be present so the runner sees structured output.
		if !contains(c.Args, "--json") {
			t.Errorf("calls[%d] missing --json: %v", i, c.Args)
		}
	}

	// Issues land first, then PRs, with stable mapping into the source.Task
	// shape.
	got := tasks[0]
	if got.Source != "github" || got.ExternalID != "owner/repo#12" {
		t.Errorf("issue task[0] = %+v", got)
	}
	if got.Title != "issue title" || got.Body != "issue body" {
		t.Errorf("issue task[0] title/body = %q / %q", got.Title, got.Body)
	}
	if got.SourcePath != "https://github.com/owner/repo/issues/12" {
		t.Errorf("issue task[0] SourcePath = %q", got.SourcePath)
	}
	if got.Done {
		t.Errorf("issue task[0] Done should be false (is:open query)")
	}
	if got.RawMetadata["type"] != "issue" {
		t.Errorf("issue task[0] type = %v", got.RawMetadata["type"])
	}
	if got.RawMetadata["updated_at"] != "2026-01-02T03:04:05Z" {
		t.Errorf("issue task[0] updated_at = %v", got.RawMetadata["updated_at"])
	}

	got = tasks[1]
	if got.ExternalID != "owner/repo#34" || got.RawMetadata["type"] != "pr" {
		t.Errorf("pr task[1] = %+v", got)
	}
}

func TestListEmptyArrayReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: "[]"},
		{stdout: "[]"},
	}}
	p := New(WithRunner(fr))
	tasks, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("len = %d, want 0", len(tasks))
	}
}

func TestListMalformedJSONReturnsErrInvalidResponse(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: "{not json"},
	}}
	p := New(WithRunner(fr))
	_, err := p.List(context.Background())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
}

func TestListPropagatesRunnerError(t *testing.T) {
	t.Parallel()

	boom := errors.New("gh: rate limited")
	fr := &fakeRunner{responses: []runResponse{
		{err: boom},
	}}
	p := New(WithRunner(fr))
	_, err := p.List(context.Background())
	if err == nil {
		t.Fatalf("err = nil, want runner error to propagate")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want errors.Is(err, boom)", err)
	}
}

func TestSinceEmptyCheckpointBehavesLikeList(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: "[]"},
		{stdout: "[]"},
	}}
	p := New(WithRunner(fr))
	if _, err := p.Since(context.Background(), ""); err != nil {
		t.Fatalf("Since: %v", err)
	}
	for i, c := range fr.calls {
		if c.Args[2] != "is:open assignee:@me" {
			t.Errorf("calls[%d] query = %q (must not include `updated:` qualifier)", i, c.Args[2])
		}
	}
}

func TestSinceCheckpointAppendsUpdatedQualifier(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: "[]"},
		{stdout: "[]"},
	}}
	p := New(WithRunner(fr))
	if _, err := p.Since(context.Background(), "2026-01-02T03:04:05Z"); err != nil {
		t.Fatalf("Since: %v", err)
	}
	want := "is:open assignee:@me updated:>=2026-01-02T03:04:05Z"
	for i, c := range fr.calls {
		if c.Args[2] != want {
			t.Errorf("calls[%d] query = %q, want %q", i, c.Args[2], want)
		}
	}
}

func TestCompleteRunsGhIssueClose(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: ""},
	}}
	p := New(WithRunner(fr))
	if err := p.Complete(context.Background(), "owner/repo#42"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(fr.calls), fr.calls)
	}
	c := fr.calls[0]
	if c.Name != "gh" {
		t.Errorf("Name = %q, want gh", c.Name)
	}
	want := []string{"issue", "close", "42", "--repo", "owner/repo"}
	if !equalStrings(c.Args, want) {
		t.Errorf("Args = %v, want %v", c.Args, want)
	}
}

func TestCompleteWithCommentRunsCommentThenClose(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{stdout: ""}, // comment
		{stdout: ""}, // close
	}}
	p := New(WithRunner(fr), WithCompletionComment("done by marunage"))
	if err := p.Complete(context.Background(), "owner/repo#7"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("calls = %d, want 2: %+v", len(fr.calls), fr.calls)
	}
	wantComment := []string{"issue", "comment", "7", "--repo", "owner/repo", "--body", "done by marunage"}
	if !equalStrings(fr.calls[0].Args, wantComment) {
		t.Errorf("comment Args = %v, want %v", fr.calls[0].Args, wantComment)
	}
	wantClose := []string{"issue", "close", "7", "--repo", "owner/repo"}
	if !equalStrings(fr.calls[1].Args, wantClose) {
		t.Errorf("close Args = %v, want %v", fr.calls[1].Args, wantClose)
	}
}

func TestCompleteRejectsMalformedExternalID(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{}
	p := New(WithRunner(fr))
	for _, bad := range []string{"", "owner/repo", "owner#42", "owner/repo#", "owner/repo#abc", "ownerrepo#1"} {
		err := p.Complete(context.Background(), bad)
		if !errors.Is(err, ErrInvalidExternalID) {
			t.Errorf("Complete(%q) = %v, want ErrInvalidExternalID", bad, err)
		}
	}
	if len(fr.calls) != 0 {
		t.Errorf("runner should not be invoked for invalid externalID; calls = %+v", fr.calls)
	}
}

func TestCompletePropagatesCloseError(t *testing.T) {
	t.Parallel()

	boom := errors.New("gh: not authorised")
	fr := &fakeRunner{responses: []runResponse{
		{err: boom, stderr: "permission denied"},
	}}
	p := New(WithRunner(fr))
	err := p.Complete(context.Background(), "owner/repo#1")
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want errors.Is(err, boom)", err)
	}
}

func TestAuthStatusZeroExitMeansAuthenticated(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{{stdout: "Logged in"}}}
	p := New(WithRunner(fr))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q, want authenticated", got)
	}
	if len(fr.calls) != 1 || fr.calls[0].Name != "gh" || !equalStrings(fr.calls[0].Args, []string{"auth", "status"}) {
		t.Errorf("calls = %+v", fr.calls)
	}
}

func TestAuthStatusNonZeroExitMeansNotConfigured(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{{err: errors.New("exit 1")}}}
	p := New(WithRunner(fr))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("AuthStatus = %q, want not_configured", got)
	}
}

func TestAuthStatusMissingBinaryMeansNotConfigured(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{{err: &exec.Error{Name: "gh", Err: exec.ErrNotFound}}}}
	p := New(WithRunner(fr))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("AuthStatus = %q, want not_configured", got)
	}
}

func TestSetupNonInteractiveRequiresAuth(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{err: errors.New("not logged in")}, // gh auth status fails
	}}
	p := New(WithRunner(fr))
	err := p.Setup(context.Background(), source.SetupOptions{NonInteractive: true})
	if !errors.Is(err, ErrInteractiveSetupRequired) {
		t.Fatalf("Setup err = %v, want ErrInteractiveSetupRequired", err)
	}
}

func TestSetupAuthenticatedIsNoOp(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{{stdout: "Logged in"}}}
	p := New(WithRunner(fr))
	if err := p.Setup(context.Background(), source.SetupOptions{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
}

func TestSetupInteractivePromptsManualLogin(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{responses: []runResponse{
		{err: errors.New("not logged in")}, // gh auth status fails
	}}
	p := New(WithRunner(fr))
	err := p.Setup(context.Background(), source.SetupOptions{})
	if !errors.Is(err, ErrInteractiveSetupRequired) {
		t.Fatalf("Setup err = %v, want ErrInteractiveSetupRequired", err)
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("err should hint at `gh auth login`, got %v", err)
	}
}

// --- helpers ---

func contains(args []string, needle string) bool {
	for _, a := range args {
		if a == needle {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
