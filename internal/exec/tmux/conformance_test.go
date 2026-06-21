package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/exec/exectest"
	exectmux "github.com/haruotsu/marunage/internal/exec/tmux"
)

// tmuxHarness drives the shared conformance suite against the tmux backend,
// reusing the package's fakeRunner so no real tmux is spawned.
type tmuxHarness struct{}

func (tmuxHarness) Healthy(*testing.T) exec.Executor {
	fr := &fakeRunner{newSessionOut: "marunage-conformance\n", capture: []string{readyPane}}
	return exectmux.New(fastOpts(fr)...)
}

func (tmuxHarness) CreateFails(*testing.T) exec.Executor {
	return exectmux.New(fastOpts(&fakeRunner{newSessionErr: errors.New("tmux create boom")})...)
}

func (tmuxHarness) ReadinessFails(*testing.T) exec.Executor {
	fr := &fakeRunner{newSessionOut: "marunage-conformance\n", capture: []string{"booting..."}}
	return exectmux.New(fastOpts(fr)...)
}

func (tmuxHarness) Completed(t *testing.T, exitCode int) (exec.Executor, exec.Session) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte(strconv.Itoa(exitCode)), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := exectmux.New(exectmux.WithRunner(&fakeRunner{}), exectmux.WithPollInterval(time.Millisecond))
	return e, exec.NewSession("marunage-conformance", exectmux.Handle{SentinelDir: dir})
}

func TestTmuxConformance(t *testing.T) {
	exectest.RunConformance(t, tmuxHarness{})
}
