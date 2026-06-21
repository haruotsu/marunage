package cmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	cmuxclient "github.com/haruotsu/marunage/internal/cmux"
	"github.com/haruotsu/marunage/internal/exec"
	execcmux "github.com/haruotsu/marunage/internal/exec/cmux"
	"github.com/haruotsu/marunage/internal/exec/exectest"
)

// cmuxHarness drives the shared conformance suite against the cmux backend,
// reusing the package's fakeClient so no real cmux is spawned.
type cmuxHarness struct{}

func (cmuxHarness) Healthy(*testing.T) exec.Executor {
	return execcmux.New(&fakeClient{})
}

func (cmuxHarness) CreateFails(*testing.T) exec.Executor {
	return execcmux.New(&fakeClient{
		newWorkspaceHook: func(cmuxclient.NewWorkspaceOptions) (cmuxclient.Workspace, error) {
			return cmuxclient.Workspace{}, errors.New("cmux create boom")
		},
	})
}

func (cmuxHarness) ReadinessFails(*testing.T) exec.Executor {
	return execcmux.New(&fakeClient{waitReadyErr: cmuxclient.ErrTimeout})
}

func (cmuxHarness) Completed(t *testing.T, exitCode int) (exec.Executor, exec.Session) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte(strconv.Itoa(exitCode)), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execcmux.New(&fakeClient{}, execcmux.WithPollInterval(time.Millisecond))
	return e, exec.NewSession("workspace:1", execcmux.Handle{SentinelDir: dir})
}

func TestCmuxConformance(t *testing.T) {
	exectest.RunConformance(t, cmuxHarness{})
}
