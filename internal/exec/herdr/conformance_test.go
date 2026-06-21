package herdr_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/haruotsu/marunage/internal/exec"
	"github.com/haruotsu/marunage/internal/exec/exectest"
	execherdr "github.com/haruotsu/marunage/internal/exec/herdr"
)

// herdrHarness drives the shared conformance suite against the herdr backend,
// reusing the package's herdrFake so no real herdr is spawned.
type herdrHarness struct{}

func (herdrHarness) Healthy(*testing.T) exec.Executor {
	return execherdr.New(fastOpts(&herdrFake{createOut: createOK, read: []string{readyPane}})...)
}

func (herdrHarness) CreateFails(*testing.T) exec.Executor {
	return execherdr.New(fastOpts(&herdrFake{createErr: errors.New("herdr create boom")})...)
}

func (herdrHarness) ReadinessFails(*testing.T) exec.Executor {
	return execherdr.New(fastOpts(&herdrFake{createOut: createOK, read: []string{"booting..."}})...)
}

func (herdrHarness) Completed(t *testing.T, exitCode int) (exec.Executor, exec.Session) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exec.SentinelFile), []byte(strconv.Itoa(exitCode)), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	e := execherdr.New(execherdr.WithRunner(&herdrFake{}), execherdr.WithPollInterval(time.Millisecond))
	return e, exec.NewSession("1-1", execherdr.Handle{SentinelDir: dir})
}

func TestHerdrConformance(t *testing.T) {
	exectest.RunConformance(t, herdrHarness{})
}
