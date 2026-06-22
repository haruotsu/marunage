package cli

import (
	"fmt"
	"os/exec"

	"github.com/spf13/cobra"
)

// openViewerHook launches the cmux markdown viewer on the rendered view.md.
// A package var so tests assert "render then launch" without a real cmux.
var openViewerHook = openInCmux

// openInCmux opens path in cmux. cmux is absent in many environments (CI, a
// plain shell), so a missing binary is reported as an error the command turns
// into a soft "here is the path" message rather than a hard failure.
func openInCmux(path string) error {
	if _, err := exec.LookPath("cmux"); err != nil {
		return fmt.Errorf("cmux not found on PATH")
	}
	return exec.Command("cmux", path).Run()
}

// newOpenCmd builds `marunage open`: render view.md, then show it in cmux's
// markdown viewer. When cmux cannot be launched the resolved path is still
// printed so the user can open it themselves — `open` never blocks on cmux.
func newOpenCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "open",
		Short:        "Render view.md and open it in cmux's markdown viewer.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dest, err := writeViewFile(cmd.Context(), *configPath)
			if err != nil {
				return err
			}
			if err := openViewerHook(dest); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n(could not open in cmux: %v — open the file manually)\n", dest, err)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), dest)
			return nil
		},
	}
}
