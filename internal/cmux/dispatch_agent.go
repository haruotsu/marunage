package cmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNoCmuxSession is returned by DispatchAgent.Start when cmux is not
// accessible (the process is not running inside a cmux terminal session).
var ErrNoCmuxSession = errors.New("cmux: no session — dispatch agent requires a cmux terminal")

// DispatchAgent enables web-triggered dispatch from processes that may be
// running outside a cmux terminal session (orphaned daemons, etc.).
//
// How it works:
//
//  1. Start() creates a dedicated cmux workspace that runs an agent shell
//     loop.  The loop polls a queue directory and runs "marunage dispatch
//     <id>" for every *.dispatch file it finds.  Because the workspace is a
//     live cmux terminal its session check always passes.
//
//  2. Dispatch() simply writes an empty <id>.dispatch file to the queue
//     directory.  This works even from orphaned/daemonised processes because
//     file I/O never requires a cmux session.
//
//  3. The agent workspace persists beyond the web-server's own terminal
//     session, so dispatch continues to work even after the user closes the
//     tab that started "marunage web".
//
// Lifecycle: call Start() once at web-server startup (returns
// ErrNoCmuxSession if cmux is not accessible — callers should fall back to
// direct dispatch in that case), then call Dispatch() from the web handler.
type DispatchAgent struct {
	queueDir string // dir where *.dispatch files are written
	wsFile   string // file storing the live agent workspace ref ("workspace:N")
	runner   Runner // injectable for tests; nil → ExecRunner
	exePath  string // absolute path to the marunage binary
	cfgPath  string // --config path forwarded to the agent loop
}

// NewDispatchAgent constructs a DispatchAgent.  queueDir and wsFile are
// paths under the marunage state directory; exePath and cfgPath are
// forwarded to the agent workspace so it can invoke "marunage dispatch".
func NewDispatchAgent(queueDir, wsFile, exePath, cfgPath string) *DispatchAgent {
	return &DispatchAgent{
		queueDir: queueDir,
		wsFile:   wsFile,
		runner:   ExecRunner{},
		exePath:  exePath,
		cfgPath:  cfgPath,
	}
}

// Start creates the agent workspace if one is not already running.
// Returns ErrNoCmuxSession when cmux is not accessible.
func (a *DispatchAgent) Start(ctx context.Context) error {
	r := a.effectiveRunner()

	if err := os.MkdirAll(a.queueDir, 0o700); err != nil {
		return fmt.Errorf("dispatch agent: mkdir queue: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.wsFile), 0o700); err != nil {
		return fmt.Errorf("dispatch agent: mkdir ws dir: %w", err)
	}

	// Check whether the previously recorded agent workspace is still alive.
	if existing, ok := a.readWsFile(); ok {
		if a.workspaceAlive(ctx, r, existing) {
			return nil // reuse existing agent
		}
	}

	// Build the agent shell command: a tight poll loop that dispatches
	// every *.dispatch file it finds in queueDir.
	agentCmd := a.buildAgentCmd()

	stdout, stderr, err := r.Run(ctx, "cmux",
		"new-workspace",
		"--cwd", filepath.Dir(a.queueDir),
		"--command", agentCmd,
		"--name", "marunage-dispatch-agent",
	)
	if err != nil {
		if strings.Contains(string(stderr), "Access denied") ||
			strings.Contains(string(stderr), "Broken pipe") ||
			strings.Contains(string(stderr), "no such file") {
			return fmt.Errorf("%w: %s", ErrNoCmuxSession, strings.TrimSpace(string(stderr)))
		}
		return fmt.Errorf("dispatch agent: new-workspace: %w (stderr=%s)", err, strings.TrimSpace(string(stderr)))
	}

	wsRef := workspacePattern.FindString(string(stdout))
	if wsRef == "" {
		return fmt.Errorf("dispatch agent: unparseable workspace ref: %q", strings.TrimSpace(string(stdout)))
	}

	if err := os.WriteFile(a.wsFile, []byte(wsRef), 0o600); err != nil {
		return fmt.Errorf("dispatch agent: write ws file: %w", err)
	}
	return nil
}

// Dispatch writes an <id>.dispatch file to the queue directory. The agent
// workspace shell picks it up and runs "marunage dispatch <id>".
// Safe to call even from an orphaned/daemonised process.
func (a *DispatchAgent) Dispatch(_ context.Context, id int64) error {
	return a.Enqueue(id)
}

// Enqueue writes the dispatch sentinel file. Exported so tests can call it
// directly; production code goes through Dispatch.
func (a *DispatchAgent) Enqueue(id int64) error {
	if err := os.MkdirAll(a.queueDir, 0o700); err != nil {
		return fmt.Errorf("dispatch agent: mkdir queue: %w", err)
	}
	name := filepath.Join(a.queueDir, strconv.FormatInt(id, 10)+".dispatch")
	return os.WriteFile(name, nil, 0o600)
}

func (a *DispatchAgent) effectiveRunner() Runner {
	if a.runner != nil {
		return a.runner
	}
	return ExecRunner{}
}

func (a *DispatchAgent) readWsFile() (string, bool) {
	data, err := os.ReadFile(a.wsFile)
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	return s, s != ""
}

// workspaceAlive returns true when wsRef appears in "cmux list-workspaces".
func (a *DispatchAgent) workspaceAlive(ctx context.Context, r Runner, wsRef string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stdout, _, err := r.Run(ctx, "cmux", "list-workspaces")
	if err != nil {
		return false
	}
	return strings.Contains(string(stdout), wsRef)
}

// buildAgentCmd returns the bash one-liner the agent workspace runs.
// Using `bash -c '...'` guarantees consistent glob behaviour regardless of
// the user's default shell: bash treats an unmatched glob as a literal (the
// `[ -f "$f" ] || continue` guard then skips it), whereas zsh raises an
// error by default which would abort the loop.
func (a *DispatchAgent) buildAgentCmd() string {
	q := shellescape(a.queueDir)
	exe := shellescape(a.exePath)
	cfg := shellescape(a.cfgPath)
	inner := fmt.Sprintf(
		`while true; do `+
			`for f in %s/*.dispatch; do `+
			`[ -f "$f" ] || continue; `+
			`id=$(basename "$f" .dispatch); `+
			`rm -f "$f"; `+
			`%s --config %s dispatch "$id"; `+
			`done; `+
			`sleep 1; `+
			`done`,
		q, exe, cfg,
	)
	return "bash -c " + shellescape(inner)
}

// shellescape wraps a path in single quotes, escaping any embedded single
// quotes (replace ' with '\'').  Sufficient for shell command construction
// where the input is a filesystem path (no null bytes, controlled chars).
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
