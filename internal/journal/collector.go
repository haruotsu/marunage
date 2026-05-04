package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/haruotsu/marunage/internal/store"
)

// Collector gathers activity items from one source since a given time.
type Collector interface {
	Name() string
	Collect(ctx context.Context, since time.Time) ([]Item, error)
}

// --- GitCollector ---

// GitCollector collects git commit subjects since a given time.
type GitCollector struct {
	runner Runner
	cwd    string
}

// GitOption configures a GitCollector.
type GitOption func(*GitCollector)

// WithGitRunner injects a custom Runner (tests).
func WithGitRunner(r Runner) GitOption { return func(c *GitCollector) { c.runner = r } }

// WithGitCWD sets the working directory for git commands.
func WithGitCWD(cwd string) GitOption { return func(c *GitCollector) { c.cwd = cwd } }

// NewGitCollector returns a GitCollector with the given options.
func NewGitCollector(opts ...GitOption) *GitCollector {
	c := &GitCollector{runner: execRunner{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *GitCollector) Name() string { return "git" }

// Collect runs `git log --since=<since> --pretty=format:%s --no-merges`
// and returns one Item per commit subject line.
func (c *GitCollector) Collect(ctx context.Context, since time.Time) ([]Item, error) {
	args := []string{
		"log",
		"--since=" + since.UTC().Format(time.RFC3339),
		"--pretty=format:%s",
		"--no-merges",
	}
	if c.cwd != "" {
		args = append([]string{"-C", c.cwd}, args...)
	}
	stdout, _, err := c.runner.Run(ctx, "git", args...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseLines(stdout), nil
}

// --- TaskCollector ---

// TaskLister is the narrow store interface the TaskCollector needs.
type TaskLister interface {
	List(ctx context.Context, f store.ListFilter) ([]store.Task, error)
}

// TaskCollector collects completed/failed marunage tasks since a given time.
type TaskCollector struct {
	lister TaskLister
}

// NewTaskCollector returns a TaskCollector backed by the given TaskLister.
func NewTaskCollector(l TaskLister) *TaskCollector { return &TaskCollector{lister: l} }

func (c *TaskCollector) Name() string { return "marunage" }

// Collect queries done/failed tasks and filters by CompletedAt > since.
func (c *TaskCollector) Collect(ctx context.Context, since time.Time) ([]Item, error) {
	tasks, err := c.lister.List(ctx, store.ListFilter{
		Statuses: []string{store.StatusDone, store.StatusFailed},
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	var items []Item
	for _, t := range tasks {
		if t.CompletedAt.IsZero() || !t.CompletedAt.After(since) {
			continue
		}
		items = append(items, Item{
			Text: fmt.Sprintf("#%d %s (%s)", t.ID, t.Title, t.Status),
		})
	}
	return items, nil
}

// --- GitHubCollector ---

// GitHubCollector collects merged pull requests since a given time via `gh`.
type GitHubCollector struct {
	runner Runner
}

// GitHubOption configures a GitHubCollector.
type GitHubOption func(*GitHubCollector)

// WithGitHubRunner injects a custom Runner (tests).
func WithGitHubRunner(r Runner) GitHubOption { return func(c *GitHubCollector) { c.runner = r } }

// NewGitHubCollector returns a GitHubCollector with the given options.
func NewGitHubCollector(opts ...GitHubOption) *GitHubCollector {
	c := &GitHubCollector{runner: execRunner{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *GitHubCollector) Name() string { return "github" }

// Collect calls `gh pr list --state merged --json number,title,mergedAt`
// and returns Items for PRs merged after since.
func (c *GitHubCollector) Collect(ctx context.Context, since time.Time) ([]Item, error) {
	stdout, _, err := c.runner.Run(ctx, "gh", "pr", "list",
		"--state", "merged",
		"--json", "number,title,mergedAt",
		"--search", "merged:>="+since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	return parseGitHubPRs(stdout, since)
}

// rawPR is the JSON shape emitted by `gh pr list --json number,title,mergedAt`.
type rawPR struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	MergedAt string `json:"mergedAt"`
}

func parseGitHubPRs(stdout []byte, since time.Time) ([]Item, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var prs []rawPR
	if err := json.Unmarshal(trimmed, &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	var items []Item
	for _, pr := range prs {
		if pr.MergedAt == "" {
			continue
		}
		mergedAt, err := time.Parse(time.RFC3339, pr.MergedAt)
		if err != nil {
			continue
		}
		if !mergedAt.After(since) {
			continue
		}
		items = append(items, Item{
			Text: fmt.Sprintf("Merged PR #%d: %s", pr.Number, pr.Title),
		})
	}
	return items, nil
}

// parseLines splits stdout bytes into non-empty trimmed line Items.
func parseLines(stdout []byte) []Item {
	raw := strings.TrimRight(string(stdout), "\n")
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	items := make([]Item, 0, len(lines))
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s != "" {
			items = append(items, Item{Text: s})
		}
	}
	return items
}
