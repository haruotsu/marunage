// Package backend is the one place that maps the [execution].executor config
// value onto a concrete exec.Executor. It lives above the exec package (it
// imports the backend implementations, which import exec) so the selection
// logic stays inside the execution layer rather than leaking the list of
// backends into cmd/marunage. PR-R05 wires the loop/dispatch call sites onto
// backend.New; until then this is the single, tested seam that proves the
// abstraction supports more than cmux.
package backend

import (
	"fmt"

	cmuxclient "github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/exec"
	execcmux "github.com/haruotsu/marunage/internal/exec/cmux"
	exectmux "github.com/haruotsu/marunage/internal/exec/tmux"
)

// ErrUnknownExecutor is returned by New for an executor name no backend
// implements. config validation already rejects unknown names at load time;
// this guards the seam against a name that is allowed by config but not yet
// wired here (e.g. "local" before PR-R08).
var ErrUnknownExecutor = fmt.Errorf("exec/backend: unknown executor")

// New constructs the execution backend named by the config value. An empty
// name defaults to cmux, the historical backend, so a config predating the
// [execution].executor key keeps its behaviour unchanged. The cmux backend
// is wired with the Claude readiness probe (the dispatch path); tmux runs
// the system tmux through its default ExecRunner.
func New(executor string) (exec.Executor, error) {
	switch executor {
	case "", "cmux":
		return execcmux.New(cmuxclient.NewClient(
			cmuxclient.WithReadinessProbe(cmuxclient.NewClaudeReadinessProbe()),
		)), nil
	case "tmux":
		return exectmux.New(), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownExecutor, executor)
	}
}
