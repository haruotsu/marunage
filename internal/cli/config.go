package cli

import (
	"fmt"
	"os"
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

// newConfigCmd builds the `marunage config` subtree. get/set are wired to
// the internal/config primitives; edit/wizard remain stubs (PR-30+ will
// flesh them out).
func newConfigCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect or modify ~/.marunage/config.toml.",
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
			defer auditor.Close()

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

	for _, s := range []stubSpec{
		{"edit", "Open ~/.marunage/config.toml in $EDITOR with schema validation on save."},
		{"wizard", "Run the interactive config wizard."},
	} {
		cmd.AddCommand(newStubCmd(s, "config"))
	}
	return cmd
}
