package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
)

// defaultConfigPath returns ~/.marunage/config.toml, with a graceful fallback
// when $HOME cannot be resolved (e.g. exotic CI environments). Subcommands
// surface the resolved path in their error messages so the user can tell
// where the binary was looking.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".marunage/config.toml"
	}
	return filepath.Join(home, ".marunage", "config.toml")
}

// auditLogPathFor derives the audit log location from the active config
// path so `--config` overrides flow through to the audit trail. Mirroring
// the documented ~/.marunage/logs/ layout next to config.toml keeps both
// files inside the same per-user data directory.
func auditLogPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "logs", "audit.log")
}

// newConfigCmd builds the `marunage config` subtree. Running `marunage config`
// without a subcommand launches the interactive wizard. get/set remain
// available for scripting.
func newConfigCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configure marunage interactively (or use get/set for scripting).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigWizard(*configPath, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Print the value of a single config key.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}
			val, err := config.Get(c, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), val)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a single config key (non-interactive).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}
			if err := config.Set(&c, args[0], args[1]); err != nil {
				return err
			}

			// Open the audit writer only after Set succeeds so a typo'd
			// key never creates an empty logs/ tree on disk and never
			// emits a misleading config.set line.
			auditPath := auditLogPathFor(*configPath)
			auditor, err := logging.NewAuditLog(auditPath)
			if err != nil {
				return fmt.Errorf("open audit log %s: %w", auditPath, err)
			}
			defer func() { _ = auditor.Close() }()

			auditor.Record(config.AuditEvent{
				Action: "config.set",
				Path:   *configPath,
				Key:    args[0],
				Value:  args[1],
			})
			if err := config.Save(*configPath, c, auditor); err != nil {
				return fmt.Errorf("save %s: %w", *configPath, err)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "wizard",
		Short: "Run the interactive config wizard (same as `marunage config` with no args).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigWizard(*configPath, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:          "edit",
		Short:        "Open config.toml in $EDITOR; validate on save and roll back if invalid.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigEdit(*configPath, cmd.OutOrStdout())
		},
	})
	return cmd
}

// editFileHook launches the user's editor on path. It is a package var so
// tests can substitute a scripted editor instead of spawning a real one.
var editFileHook = launchEditor

// launchEditor opens path in $EDITOR (falling back to $VISUAL, then vi),
// inheriting the terminal so the user can edit interactively.
func launchEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// runConfigEdit opens config.toml in the editor and, after it exits, re-loads
// + validates the file. On a parse/validation failure it restores the pre-edit
// contents so an invalid config never lands on disk, preserving the user's
// comments and formatting (the editor writes the raw file in place; we never
// re-serialise). A successful edit is recorded to audit.log.
func runConfigEdit(configPath string, out io.Writer) error {
	original, readErr := os.ReadFile(configPath)
	existed := readErr == nil
	// Only a genuine "file is absent" read error means we are creating a new
	// config. Any other read failure (permissions, EISDIR, transient I/O) must
	// abort the edit: treating it as "did not exist" would let the rollback's
	// os.Remove delete a real, still-present config (data loss).
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", configPath, readErr)
	}

	// Capture the pre-edit permission mode so a rollback restores it exactly,
	// rather than re-creating the file under a hard-coded mode.
	origMode := fs.FileMode(0o600)
	if existed {
		if info, err := os.Stat(configPath); err == nil {
			origMode = info.Mode().Perm()
		}
	}

	if err := editFileHook(configPath); err != nil {
		return fmt.Errorf("launch editor: %w", err)
	}

	c, err := config.Load(configPath)
	if err != nil {
		return rollbackConfig(configPath, original, existed, origMode, fmt.Errorf("invalid config after edit (rolled back): %w", err))
	}
	if err := c.Validate(); err != nil {
		return rollbackConfig(configPath, original, existed, origMode, fmt.Errorf("invalid config after edit (rolled back): %w", err))
	}

	if auditor, err := logging.NewAuditLog(auditLogPathFor(configPath)); err == nil {
		auditor.Record(config.AuditEvent{Action: "config.edit", Path: configPath})
		_ = auditor.Close()
	}
	_, _ = fmt.Fprintln(out, "Config saved.")
	return nil
}

// rollbackConfig restores the pre-edit file (or removes it when the edit
// created a previously-absent file) and returns cause. The restore is atomic
// (tmp file in the same dir + rename) so a crash mid-rollback can never leave a
// half-written config on disk, and it re-applies the original permission mode.
func rollbackConfig(configPath string, original []byte, existed bool, mode fs.FileMode, cause error) error {
	if !existed {
		_ = os.Remove(configPath)
		return cause
	}
	_ = atomicWriteFileMode(configPath, original, mode)
	return cause
}

// atomicWriteFileMode writes body to path via a sibling tmp file + rename, so
// readers never observe a partial file, and chmods the result to mode before
// the rename publishes it.
func atomicWriteFileMode(path string, body []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
