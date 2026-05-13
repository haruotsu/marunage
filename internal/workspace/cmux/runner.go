package cmux

import "github.com/haruotsu/marunage/internal/workspace"

// Runner and ExecRunner moved to the workspace package so cmux and
// herdr can share one Runner implementation. These aliases keep
// `cmux.ExecRunner{}` and `cmux.Runner` working at every call site that
// existed before the refactor; isBinaryNotFound's exported equivalent
// is workspace.IsBinaryNotFound.
type (
	Runner     = workspace.Runner
	ExecRunner = workspace.ExecRunner
)
