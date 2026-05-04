package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
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

	cmd.AddCommand(newConfigEditCmd(configPath))
	cmd.AddCommand(newConfigWizardCmd(configPath))
	return cmd
}

// newConfigEditCmd opens the active config.toml in $EDITOR (fallback: vi),
// validates the result, and atomically writes it back on success. The tmp
// file is kept on validation failure so the user can correct it.
func newConfigEditCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:          "edit",
		Short:        "Open ~/.marunage/config.toml in $EDITOR with schema validation on save.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}

			if err := os.MkdirAll(filepath.Dir(*configPath), 0o755); err != nil {
				return fmt.Errorf("mkdir: %w", err)
			}

			tmp, err := os.CreateTemp(filepath.Dir(*configPath), "config.toml.edit.*")
			if err != nil {
				return fmt.Errorf("create tmp: %w", err)
			}
			tmpName := tmp.Name()

			body, marshalErr := toml.Marshal(c)
			if marshalErr != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpName)
				return fmt.Errorf("marshal config: %w", marshalErr)
			}
			if _, err := tmp.Write(body); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpName)
				return fmt.Errorf("write tmp: %w", err)
			}
			if err := tmp.Close(); err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("close tmp: %w", err)
			}

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			parts := strings.Fields(editor)
			parts = append(parts, tmpName)
			editorCmd := exec.Command(parts[0], parts[1:]...) //nolint:gosec
			editorCmd.Stdin = cmd.InOrStdin()
			editorCmd.Stdout = cmd.OutOrStdout()
			editorCmd.Stderr = cmd.ErrOrStderr()
			if err := editorCmd.Run(); err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("editor: %w", err)
			}

			edited, err := config.Load(tmpName)
			if err != nil {
				return fmt.Errorf("validation failed (edited file kept at %s): %w", tmpName, err)
			}

			auditPath := auditLogPathFor(*configPath)
			auditor, err := logging.NewAuditLog(auditPath)
			if err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("open audit log %s: %w", auditPath, err)
			}
			defer func() { _ = auditor.Close() }()

			auditor.Record(config.AuditEvent{Action: "config.edit", Path: *configPath})
			if err := config.Save(*configPath, edited, auditor); err != nil {
				_ = os.Remove(tmpName)
				return fmt.Errorf("save %s: %w", *configPath, err)
			}

			_ = os.Remove(tmpName)
			return nil
		},
	}
}

// wizardSection describes a group of keys surfaced by `config wizard`.
type wizardSection struct {
	name string
	keys []string
}

var wizardSections = []wizardSection{
	{"core", []string{"core.max_parallel", "core.log_level", "core.db_path"}},
	{"secrets", []string{"secrets.backend"}},
	{"discovery", []string{"discovery.interval", "discovery.sources_enabled"}},
	{"execution", []string{
		"execution.permission_mode",
		"execution.claude_command",
		"execution.on_unknown_permission",
		"execution.human_wait_timeout",
	}},
	{"reflection", []string{"reflection.enabled", "reflection.sample_rate"}},
}

// newConfigWizardCmd provides an interactive prompt-driven config editor.
// --section limits the run to one named section; omitting it runs all sections.
func newConfigWizardCmd(configPath *string) *cobra.Command {
	var section string

	cmd := &cobra.Command{
		Use:          "wizard",
		Short:        "Run the interactive config wizard.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var sections []wizardSection
			if section == "" {
				sections = wizardSections
			} else {
				for _, s := range wizardSections {
					if s.name == section {
						sections = []wizardSection{s}
						break
					}
				}
				if len(sections) == 0 {
					names := make([]string, len(wizardSections))
					for i, s := range wizardSections {
						names[i] = s.name
					}
					return fmt.Errorf("unknown section %q; valid sections: %s", section, strings.Join(names, ", "))
				}
			}

			c, err := config.Load(*configPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", *configPath, err)
			}

			reader := bufio.NewReader(activeStdinReader())
			out := cmd.OutOrStdout()

			for _, sec := range sections {
				if err := runWizardSection(reader, out, &c, sec); err != nil {
					return err
				}
			}

			auditPath := auditLogPathFor(*configPath)
			auditor, err := logging.NewAuditLog(auditPath)
			if err != nil {
				return fmt.Errorf("open audit log %s: %w", auditPath, err)
			}
			defer func() { _ = auditor.Close() }()

			auditor.Record(config.AuditEvent{Action: "config.wizard", Path: *configPath})
			if err := config.Save(*configPath, c, auditor); err != nil {
				return fmt.Errorf("save %s: %w", *configPath, err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&section, "section", "", "Section to configure: core | secrets | discovery | execution | reflection.")
	return cmd
}

// runWizardSection prompts the user for each key in sec and applies non-empty
// responses via config.Set. An empty response keeps the current value.
func runWizardSection(reader *bufio.Reader, out io.Writer, c *config.Config, sec wizardSection) error {
	fmt.Fprintf(out, "\n[%s]\n", sec.name)
	for _, key := range sec.keys {
		cur, err := config.Get(*c, key)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  %s [current: %s]: ", key, cur)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			// EOF with no content — keep current value.
			continue
		}
		val := strings.TrimRight(line, "\r\n")
		if val == "" {
			continue
		}
		if err := config.Set(c, key, val); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}
