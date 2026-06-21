package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner abstracts shelling out to the claude binary so the scorer is unit-
// testable without invoking real Claude. The shape mirrors the Runner idiom
// used across internal/journal and internal/source/*, but threads stdin: the
// prompt (skill body + candidate batch) can be large, so it is piped rather
// than passed on argv.
type Runner interface {
	Run(ctx context.Context, stdin []byte, name string, args ...string) (stdout, stderr []byte, err error)
}

// execRunner is the production Runner: it pipes the prompt to the claude CLI
// in headless print mode and captures stdout. exec.CommandContext kills the
// process if ctx is cancelled (e.g. the loop tick times out). It stays
// unexported (unlike the exported ExecRunner in journal/source) because the
// stdin-threading variant is specific to the scorer and NewClaudeScorer is its
// only constructor — no external package needs to instantiate it.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// ClaudeScorer is the production LLMScorer (redesign §3.5): it builds a prompt
// from the user-customisable marunage-manage SKILL.md plus the candidate batch,
// runs `claude -p` once per batch, and parses the JSON array Claude returns.
// It is the seam's only side-effecting implementation; everything else in
// manage stays pure and deterministic.
//
// Trust boundary: the candidate bodies it embeds and Claude's output both
// cross a trust boundary. The output is fully validated by parseScoreResults
// (well-formed array, every index present exactly once, in range); anything
// off-contract returns an error so Plan falls back to the deterministic stub
// rather than acting on a malformed or hostile response (No silent loss).
type ClaudeScorer struct {
	runner    Runner
	command   string
	args      []string
	skillPath string
}

// ClaudeScorerOption configures a ClaudeScorer.
type ClaudeScorerOption func(*ClaudeScorer)

// WithScorerRunner overrides the command runner (tests inject a fake).
func WithScorerRunner(r Runner) ClaudeScorerOption {
	return func(s *ClaudeScorer) {
		if r != nil {
			s.runner = r
		}
	}
}

// WithScorerCommand sets the claude binary and its headless args. Defaults to
// `claude -p` (print mode). A permission-mode flag is intentionally NOT
// applied here: scoring only reads, so it runs in the safest headless mode
// regardless of the dispatcher's execution.claude_command.
func WithScorerCommand(name string, args ...string) ClaudeScorerOption {
	return func(s *ClaudeScorer) {
		if name != "" {
			s.command = name
			s.args = args
		}
	}
}

// WithScorerSkillPath points the scorer at the on-disk marunage-manage
// SKILL.md whose judgment criteria are prepended to the prompt. Absent or
// unreadable, the scorer falls back to a terse built-in instruction so scoring
// still works on a fresh install that has not run `setup --skills` yet.
func WithScorerSkillPath(p string) ClaudeScorerOption {
	return func(s *ClaudeScorer) { s.skillPath = p }
}

// NewClaudeScorer builds a ClaudeScorer with production defaults (the real
// exec runner, `claude -p`, no skill path).
func NewClaudeScorer(opts ...ClaudeScorerOption) *ClaudeScorer {
	s := &ClaudeScorer{
		runner:  execRunner{},
		command: "claude",
		args:    []string{"-p"},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Score evaluates the whole batch in a single claude invocation (cost control:
// one call per batch, never per candidate).
func (s *ClaudeScorer) Score(ctx context.Context, items []ScoreItem) ([]ScoreResult, error) {
	if len(items) == 0 {
		return nil, nil
	}
	prompt, err := s.buildPrompt(items)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := s.runner.Run(ctx, []byte(prompt), s.command, s.args...)
	if err != nil {
		return nil, fmt.Errorf("manage: claude scorer: run: %w (stderr: %s)", err, truncate(stderr, 256))
	}
	return parseScoreResults(stdout, len(items))
}

// promptCandidate is the per-candidate shape sent to Claude. The index is the
// join key the response must echo back; the content fields are what the skill
// criteria reason over.
type promptCandidate struct {
	Index      int    `json:"index"`
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Notes      string `json:"notes"`
	Priority   string `json:"priority"`
}

const scorerInstruction = "Score every candidate above. Output ONLY a JSON array, " +
	"one object per candidate matched by index, of the form " +
	`{"index": <int>, "score": <number>, "defer": <bool>, "reason": "<one sentence>"}. ` +
	"Higher score = do sooner; defer=true means worth doing but not now. " +
	"The array length must equal the candidate count. Output no prose."

func (s *ClaudeScorer) buildPrompt(items []ScoreItem) (string, error) {
	cands := make([]promptCandidate, len(items))
	for i, it := range items {
		c := it.Candidate
		cands[i] = promptCandidate{
			Index:      i,
			Source:     c.Source,
			ExternalID: c.ExternalID,
			Title:      c.Title,
			Body:       c.Body,
			Notes:      c.Notes,
			Priority:   c.Priority,
		}
	}
	payload, err := json.Marshal(cands)
	if err != nil {
		return "", fmt.Errorf("manage: claude scorer: marshal candidates: %w", err)
	}

	var b strings.Builder
	if skill := s.readSkill(); skill != "" {
		b.WriteString(skill)
		b.WriteString("\n\n")
	}
	b.WriteString("## Candidates\n\n```json\n")
	b.Write(payload)
	b.WriteString("\n```\n\n")
	b.WriteString(scorerInstruction)
	b.WriteString("\n")
	return b.String(), nil
}

// readSkill returns the on-disk SKILL.md body, or "" when no path is set or it
// cannot be read. Best-effort by design: a missing skill degrades to the
// built-in instruction rather than failing the scoring pass.
func (s *ClaudeScorer) readSkill() string {
	if s.skillPath == "" {
		return ""
	}
	// Refuse a symlink before reading, mirroring the installer's stance
	// (internal/skills checkRegular): a symlink planted in the skills dir
	// would otherwise splice the contents of an arbitrary file into the
	// claude prompt. Lstat (not Stat) is the point — Stat would follow it.
	info, err := os.Lstat(s.skillPath)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	body, err := os.ReadFile(s.skillPath)
	if err != nil {
		return ""
	}
	return string(body)
}

type rawScore struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Defer  bool    `json:"defer"`
	Reason string  `json:"reason"`
}

// parseScoreResults extracts and validates the JSON array Claude returned,
// aligning it to a result slice of length n. It enforces the contract the
// LLMScorer seam relies on — exactly n results, every index in [0,n) present
// once — and returns an error otherwise so Plan stubs the batch.
func parseScoreResults(out []byte, n int) ([]ScoreResult, error) {
	arr := extractJSONArray(out)
	if arr == nil {
		return nil, fmt.Errorf("manage: claude scorer: no JSON array in output")
	}
	var raw []rawScore
	if err := json.Unmarshal(arr, &raw); err != nil {
		return nil, fmt.Errorf("manage: claude scorer: parse output: %w", err)
	}
	results := make([]ScoreResult, n)
	seen := make([]bool, n)
	for _, r := range raw {
		if r.Index < 0 || r.Index >= n {
			return nil, fmt.Errorf("manage: claude scorer: index %d out of range [0,%d)", r.Index, n)
		}
		if seen[r.Index] {
			return nil, fmt.Errorf("manage: claude scorer: duplicate index %d", r.Index)
		}
		seen[r.Index] = true
		results[r.Index] = ScoreResult{Score: r.Score, Defer: r.Defer, Reason: r.Reason}
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("manage: claude scorer: missing index %d", i)
		}
	}
	return results, nil
}

// extractJSONArray returns the outermost [...] span of out, tolerating prose
// the model may wrap around the array despite the instruction. It returns nil
// when no bracket pair is found; json.Unmarshal then validates the structure.
func extractJSONArray(out []byte) []byte {
	start := bytes.IndexByte(out, '[')
	end := bytes.LastIndexByte(out, ']')
	if start < 0 || end < start {
		return nil
	}
	return out[start : end+1]
}

// truncate bounds an error-embedded stderr snippet so a noisy claude failure
// does not blow up the log line.
func truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
