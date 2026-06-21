package manage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/collect"
)

type fakeRunner struct {
	stdin  []byte
	name   string
	args   []string
	stdout []byte
	stderr []byte
	err    error
}

func (f *fakeRunner) Run(_ context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	f.stdin = stdin
	f.name = name
	f.args = args
	return f.stdout, f.stderr, f.err
}

func twoItems() []ScoreItem {
	return []ScoreItem{
		{Candidate: collect.Candidate{Title: "first", Body: "a"}},
		{Candidate: collect.Candidate{Title: "second", Body: "b"}},
	}
}

func TestClaudeScorerParsesCleanArray(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":10,"defer":false,"reason":"urgent"},{"index":1,"score":2,"defer":true,"reason":"later"}]`)}
	s := NewClaudeScorer(WithScorerRunner(r), WithScorerCommand("claude", "-p"))
	got, err := s.Score(context.Background(), twoItems())
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	if got[0].Score != 10 || got[0].Defer {
		t.Errorf("result[0]=%+v, want score 10 not deferred", got[0])
	}
	if !got[1].Defer || got[1].Reason != "later" {
		t.Errorf("result[1]=%+v, want deferred reason later", got[1])
	}
	if r.name != "claude" || len(r.args) == 0 || r.args[0] != "-p" {
		t.Errorf("invoked %q %v, want claude -p", r.name, r.args)
	}
	if !strings.Contains(string(r.stdin), "first") || !strings.Contains(string(r.stdin), "second") {
		t.Errorf("prompt missing candidate content: %s", r.stdin)
	}
}

func TestClaudeScorerExtractsArrayFromProse(t *testing.T) {
	r := &fakeRunner{stdout: []byte("Here is the scoring:\n[{\"index\":0,\"score\":1},{\"index\":1,\"score\":2}]\nDone.")}
	s := NewClaudeScorer(WithScorerRunner(r))
	got, err := s.Score(context.Background(), twoItems())
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got[1].Score != 2 {
		t.Errorf("result[1].Score=%v, want 2", got[1].Score)
	}
}

func TestClaudeScorerRunnerErrorPropagates(t *testing.T) {
	r := &fakeRunner{err: errors.New("exec failed"), stderr: []byte("boom")}
	s := NewClaudeScorer(WithScorerRunner(r))
	if _, err := s.Score(context.Background(), twoItems()); err == nil {
		t.Fatal("Score must return an error when the runner fails")
	}
}

func TestClaudeScorerRejectsMissingIndex(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":1}]`)} // only one of two
	s := NewClaudeScorer(WithScorerRunner(r))
	if _, err := s.Score(context.Background(), twoItems()); err == nil {
		t.Fatal("Score must reject output that omits a candidate index")
	}
}

func TestClaudeScorerRejectsOutOfRangeIndex(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":1},{"index":5,"score":2}]`)}
	s := NewClaudeScorer(WithScorerRunner(r))
	if _, err := s.Score(context.Background(), twoItems()); err == nil {
		t.Fatal("Score must reject an out-of-range index")
	}
}

func TestClaudeScorerRejectsDuplicateIndex(t *testing.T) {
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":1},{"index":0,"score":2}]`)} // index 0 twice, index 1 missing
	s := NewClaudeScorer(WithScorerRunner(r))
	if _, err := s.Score(context.Background(), twoItems()); err == nil {
		t.Fatal("Score must reject a duplicate index")
	}
}

func TestClaudeScorerRejectsNonArrayOutput(t *testing.T) {
	r := &fakeRunner{stdout: []byte("I cannot do that")}
	s := NewClaudeScorer(WithScorerRunner(r))
	if _, err := s.Score(context.Background(), twoItems()); err == nil {
		t.Fatal("Score must reject output with no JSON array")
	}
}

func TestClaudeScorerIncludesSkillBody(t *testing.T) {
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("# MY CUSTOM CRITERIA"), 0o600); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":1},{"index":1,"score":2}]`)}
	s := NewClaudeScorer(WithScorerRunner(r), WithScorerSkillPath(skillPath))
	if _, err := s.Score(context.Background(), twoItems()); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !strings.Contains(string(r.stdin), "MY CUSTOM CRITERIA") {
		t.Errorf("prompt missing skill body: %s", r.stdin)
	}
}

func TestClaudeScorerIgnoresSymlinkedSkill(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET CONTENT"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	link := filepath.Join(dir, "SKILL.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	r := &fakeRunner{stdout: []byte(`[{"index":0,"score":1},{"index":1,"score":2}]`)}
	s := NewClaudeScorer(WithScorerRunner(r), WithScorerSkillPath(link))
	if _, err := s.Score(context.Background(), twoItems()); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if strings.Contains(string(r.stdin), "TOP SECRET") {
		t.Errorf("symlinked skill content was spliced into the prompt: %s", r.stdin)
	}
}

// The scorer satisfies the LLMScorer seam Plan injects.
var _ LLMScorer = (*ClaudeScorer)(nil)
