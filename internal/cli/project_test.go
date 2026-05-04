package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/project"
)

// fakeBoardFetcher implements boardFetcher for tests by delegating to
// project.FetchItems with a canned JSON runner — no real gh invocation.
type fakeBoardFetcher struct {
	json string
	err  error
}

func (f *fakeBoardFetcher) Fetch(ctx context.Context, parsed project.ParsedURL) ([]project.BoardItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	runner := &fakeProjectRunner{stdout: []byte(f.json)}
	return project.FetchItems(ctx, runner, parsed)
}

// fakeProjectRunner satisfies project.Runner for test doubles.
type fakeProjectRunner struct {
	stdout []byte
	err    error
}

func (r *fakeProjectRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, []byte, error) {
	return r.stdout, nil, r.err
}

func TestProjectCmd_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"project", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("project --help exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "run") {
		t.Errorf("project --help output missing 'run' subcommand:\n%s", out)
	}
}

func TestProjectRunCmd_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"project", "run", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("project run --help exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "board-url") {
		t.Errorf("project run --help output missing 'board-url':\n%s", out)
	}
}

func TestProjectRunCmd_NoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"project", "run"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("project run (no args) exit=0; want non-zero")
	}
}

func TestProjectRunCmd_InvalidURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"project", "run", "https://github.com/not-a-project"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("project run <invalid-url> exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "invalid board URL") {
		t.Errorf("project run <invalid-url> stderr=%q; want 'invalid board URL'", stderr.String())
	}
}

func TestProjectRunCmd_DryRun(t *testing.T) {
	// dry-run with a valid URL shape exits 0 after printing the fetched
	// state. We inject a fake runner so no real gh invocation happens.
	// The fake runner returns an empty board → ActionAllDone → exit 0.
	fakeEmptyBoard := `{"items":[],"totalCount":0}`
	withProjectRunnerHook(t, func(_ string) boardFetcher {
		return &fakeBoardFetcher{json: fakeEmptyBoard}
	})

	var stdout, stderr bytes.Buffer
	code := Execute(
		[]string{"project", "run",
			"https://github.com/orgs/myorg/projects/5",
			"--dry-run"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("project run --dry-run exit=%d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "complete") {
		t.Errorf("project run --dry-run stdout=%q; want 'complete'", stdout.String())
	}
}

func TestProjectRunCmd_DryRunDispatch(t *testing.T) {
	// dry-run with a board that has one Todo task: should print the task
	// title and exit without looping.
	fakeSingleTask := `{"items":[{"id":"PVTI_1","title":"Phase 1: Setup CI","status":"Todo","updatedAt":"2024-01-15T10:00:00Z"}],"totalCount":1}`
	withProjectRunnerHook(t, func(_ string) boardFetcher {
		return &fakeBoardFetcher{json: fakeSingleTask}
	})

	var stdout, stderr bytes.Buffer
	code := Execute(
		[]string{"project", "run",
			"https://github.com/orgs/myorg/projects/5",
			"--dry-run"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("project run --dry-run (dispatch) exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Phase 1: Setup CI") {
		t.Errorf("project run --dry-run stdout=%q; want task title 'Phase 1: Setup CI'", out)
	}
}

func TestProjectRunCmd_DryRunHuman(t *testing.T) {
	// dry-run with a [human] task: should print wait_human and exit.
	fakeHumanTask := `{"items":[{"id":"PVTI_h","title":"[human] Stakeholder review","status":"Todo","updatedAt":"2024-01-15T10:00:00Z"}],"totalCount":1}`
	withProjectRunnerHook(t, func(_ string) boardFetcher {
		return &fakeBoardFetcher{json: fakeHumanTask}
	})

	var stdout, stderr bytes.Buffer
	code := Execute(
		[]string{"project", "run",
			"https://github.com/orgs/myorg/projects/5",
			"--dry-run"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("project run --dry-run (human) exit=%d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "human") {
		t.Errorf("project run --dry-run (human) stdout=%q; want 'human' mention", out)
	}
}
