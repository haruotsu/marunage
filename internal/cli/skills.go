package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/skills/registry"
)

// EnvRegistryURL is the environment variable a user can set to
// override the default registry base URL without re-typing the
// --registry flag every time.
const EnvRegistryURL = "MARUNAGE_SKILLS_REGISTRY_URL"

// errSkillsRegistryNotConfigured is the typed sentinel both `install`
// and `update` raise when the operator has neither set
// MARUNAGE_SKILLS_REGISTRY_URL nor passed --registry. Surfaced as a
// cobra error so the help banner stays suppressed.
var errSkillsRegistryNotConfigured = errors.New(
	"skills: registry URL is not configured; set " + EnvRegistryURL + " or pass --registry <url>",
)

// newSkillsCmd builds the `marunage skills` subcommand tree the
// PR-203 brief promises: install / list / search / update against
// the HTTPS-based shared registry. The embedded-skill machinery owned
// by `marunage setup --skills` is left untouched; this surface only
// touches `~/.claude/skills/<name>/` for non-embedded names by
// default.
func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install / list / search / update skills from a shared registry.",
		Long: "skills wraps the HTTPS-based shared skill registry: download a\n" +
			"published SKILL.md tarball into ~/.claude/skills/<name>/ after\n" +
			"verifying its sha256, list what is currently installed, search\n" +
			"the catalog, and bump pinned versions with `update`.\n\n" +
			"The registry URL must be configured via $" + EnvRegistryURL + " or\n" +
			"--registry; there is no hard-coded default so a fresh install\n" +
			"never silently reaches out to a third-party host.",
	}

	var registryURL string
	cmd.PersistentFlags().StringVar(&registryURL, "registry", "", "Override the registry base URL (default: $"+EnvRegistryURL+").")

	cmd.AddCommand(newSkillsInstallCmd(&registryURL))
	cmd.AddCommand(newSkillsListCmd())
	cmd.AddCommand(newSkillsSearchCmd(&registryURL))
	cmd.AddCommand(newSkillsUpdateCmd(&registryURL))

	return cmd
}

func newSkillsInstallCmd(registryURL *string) *cobra.Command {
	var (
		version     string
		allowEmbed  bool
		registryArg string
	)
	cmd := &cobra.Command{
		Use:          "install <name>",
		Short:        "Install a skill from the shared registry.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := pickRegistryURL(registryArg, *registryURL)
			if base == "" {
				return errSkillsRegistryNotConfigured
			}
			root, err := skillsTargetDir()
			if err != nil {
				return err
			}
			in := &registry.Installer{
				Client:     &registry.Client{BaseURL: base, UserAgent: "marunage-cli"},
				SkillsRoot: root,
			}
			rep, err := in.Install(cmd.Context(), registry.InstallOptions{
				Name:                  args[0],
				Version:               version,
				AllowEmbeddedOverride: allowEmbed,
			})
			if err != nil {
				return err
			}
			printInstallReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Pin a specific published version (default: latest).")
	cmd.Flags().BoolVar(&allowEmbed, "allow-embedded-override", false, "Permit overwriting marunage-triage / marunage-execute / marunage-reflect.")
	cmd.Flags().StringVar(&registryArg, "registry", "", "Override the registry base URL for this command.")
	return cmd
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List skills installed via the registry.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := skillsTargetDir()
			if err != nil {
				return err
			}
			state, err := registry.LoadState(root)
			if err != nil {
				return err
			}
			printInstalledSkills(cmd.OutOrStdout(), state)
			return nil
		},
	}
}

func newSkillsSearchCmd(registryURL *string) *cobra.Command {
	var registryArg string
	cmd := &cobra.Command{
		Use:          "search [query]",
		Short:        "Search the registry catalog.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := pickRegistryURL(registryArg, *registryURL)
			if base == "" {
				return errSkillsRegistryNotConfigured
			}
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			client := &registry.Client{BaseURL: base, UserAgent: "marunage-cli"}
			idx, err := client.FetchIndex(cmd.Context())
			if err != nil {
				return err
			}
			hits := registry.Search(idx, query)
			printSearchHits(cmd.OutOrStdout(), hits)
			return nil
		},
	}
	cmd.Flags().StringVar(&registryArg, "registry", "", "Override the registry base URL for this command.")
	return cmd
}

func newSkillsUpdateCmd(registryURL *string) *cobra.Command {
	var registryArg string
	cmd := &cobra.Command{
		Use:          "update [name]",
		Short:        "Re-install installed skills (or one named skill) at the latest version.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := pickRegistryURL(registryArg, *registryURL)
			if base == "" {
				return errSkillsRegistryNotConfigured
			}
			root, err := skillsTargetDir()
			if err != nil {
				return err
			}
			state, err := registry.LoadState(root)
			if err != nil {
				return err
			}
			client := &registry.Client{BaseURL: base, UserAgent: "marunage-cli"}
			ctx := cmd.Context()

			var targets []string
			if len(args) == 1 {
				targets = []string{args[0]}
			} else {
				idx, err := client.FetchIndex(ctx)
				if err != nil {
					return err
				}
				for _, e := range registry.FindUpdates(state, idx) {
					targets = append(targets, e.Name)
				}
				if len(targets) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "All installed skills are at the latest version.")
					return nil
				}
			}

			in := &registry.Installer{Client: client, SkillsRoot: root}
			for _, name := range targets {
				rep, err := in.Install(ctx, registry.InstallOptions{Name: name})
				if err != nil {
					return fmt.Errorf("update %s: %w", name, err)
				}
				printInstallReport(cmd.OutOrStdout(), rep)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&registryArg, "registry", "", "Override the registry base URL for this command.")
	return cmd
}

// pickRegistryURL implements the --registry > $MARUNAGE_SKILLS_REGISTRY_URL >
// (parent --registry) precedence. Centralised so all three subcommands
// honour the same order.
func pickRegistryURL(localFlag, persistentFlag string) string {
	if v := strings.TrimSpace(localFlag); v != "" {
		return v
	}
	if v := strings.TrimSpace(persistentFlag); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(EnvRegistryURL))
}

func printInstallReport(w io.Writer, rep registry.InstallReport) {
	if rep.OldVersion == "" {
		fmt.Fprintf(w, "Installed: %s (version %s) -> %s\n", rep.Name, rep.NewVersion, rep.Path)
		return
	}
	fmt.Fprintf(w, "Updated:   %s (%s -> %s) -> %s\n", rep.Name, rep.OldVersion, rep.NewVersion, rep.Path)
}

func printInstalledSkills(w io.Writer, state registry.State) {
	if len(state.Installed) == 0 {
		fmt.Fprintln(w, "No registry-installed skills.")
		return
	}
	for _, e := range state.Installed {
		src := e.Source
		if src == "" {
			src = "(unknown source)"
		}
		fmt.Fprintf(w, "%s\tversion=%s\tsource=%s\n", e.Name, e.Version, src)
	}
}

func printSearchHits(w io.Writer, hits []registry.IndexEntry) {
	if len(hits) == 0 {
		fmt.Fprintln(w, "No skills matched.")
		return
	}
	for _, e := range hits {
		desc := e.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(w, "%s\tlatest=%s\t%s\n", e.Name, e.Latest, desc)
	}
}

