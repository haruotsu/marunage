package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/render"
	"github.com/haruotsu/marunage/internal/store"
)

// defaultViewPath is the on-disk destination promised by docs/requirement.md
// "ファイルレイアウト" (`~/.marunage/view.md`). It is resolved lazily so a
// process that never invokes `render` does not pay for the os.UserHomeDir()
// call.
const defaultViewPath = "~/.marunage/view.md"

// viewPathHook overrides the resolved destination path so tests can write
// into a t.TempDir without touching the real home directory. Production
// callers see the empty string and fall back to defaultViewPath.
var viewPathHook string

// withViewPath swaps in path as the active destination and restores the
// prior hook on test completion, mirroring withTaskRepoFactory.
func withViewPath(t interface{ Cleanup(func()) }, path string) {
	prev := viewPathHook
	viewPathHook = path
	t.Cleanup(func() { viewPathHook = prev })
}

// renderClockHook overrides the "_Generated:_" timestamp source so tests
// can pin the rendered body byte-for-byte. nil means "use time.Now".
var renderClockHook func() time.Time

// withRenderClock swaps in clk as the active clock for the duration of t.
func withRenderClock(t interface{ Cleanup(func()) }, clk func() time.Time) {
	prev := renderClockHook
	renderClockHook = clk
	t.Cleanup(func() { renderClockHook = prev })
}

// activeRenderClock returns the test override if installed, otherwise
// time.Now. Kept as a function value so the hook is consulted on every
// call (a stored time.Time would freeze across hook swaps).
func activeRenderClock() time.Time {
	if renderClockHook != nil {
		return renderClockHook()
	}
	return time.Now()
}

// activeViewPath returns the test override or, otherwise, the default path
// with `~` expanded against the current user's home directory.
func activeViewPath() (string, error) {
	if viewPathHook != "" {
		return viewPathHook, nil
	}
	return expandHome(defaultViewPath)
}

// newTaskRenderCmd builds `marunage render`. The command reads every task
// via the standard taskRepoFactory seam, formats the body through the
// shared render.Render() function (so `marunage open` and the future Web
// UI can reuse the same layout), and writes ~/.marunage/view.md atomically.
//
// Echoing the resolved path on stdout lets a shell pipeline chain on it
// without re-deriving the location, e.g. `bat "$(marunage render)"`.
func newTaskRenderCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "render",
		Short:        "Generate ~/.marunage/view.md for the cmux markdown viewer.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dest, err := writeViewFile(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), dest)
			return nil
		},
	}
}

// writeViewFile renders every task to ~/.marunage/view.md atomically and
// returns the resolved destination path. Shared by `render` and `open` so the
// two never drift on layout or location.
func writeViewFile(ctx context.Context, configPath string) (string, error) {
	repo, closer, err := activeTaskRepoFactory()(ctx, configPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = closer() }()

	// An empty ListFilter returns every row regardless of status — view.md is
	// the "single screen of everything" surface, not a filtered queue view.
	rows, err := repo.List(ctx, store.ListFilter{})
	if err != nil {
		return "", translateRepoError(err)
	}

	dest, err := activeViewPath()
	if err != nil {
		return "", fmt.Errorf("resolve view path: %w", err)
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create view dir: %w", err)
	}
	// MkdirAll does not narrow an existing directory's mode, so retighten in
	// case a previous run (or the user's umask) left it world-readable.
	if err := os.Chmod(parent, 0o700); err != nil {
		return "", fmt.Errorf("chmod view dir: %w", err)
	}
	if err := atomicWriteViewFile(dest, []byte(render.Render(rows, activeRenderClock()))); err != nil {
		return "", err
	}
	return dest, nil
}

// atomicWriteViewFile drops body at path via a sibling tmp file + rename,
// mirroring internal/source/markdown.atomicWriteFile. Re-implemented here
// rather than imported to avoid pulling internal/source/markdown into the
// CLI dependency graph just for one helper; the markdown package's writer
// is the canonical reference if either ever needs to change.
func atomicWriteViewFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename tmp: %w", err)
	}
	return nil
}
