package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"github.com/haruotsu/marunage/internal/skills"
)

// newSetupCmd builds the `marunage setup` subcommand. PR-34 implements
// only the `--skills` arm; the documented `--from <name>` (Phase 4
// shared registry) and bare `marunage setup` (the full interactive
// wizard) land in later PRs.
//
// The command is intentionally strict: invoking it with no recognised
// sub-flag returns a typed error so a fat-fingered run never silently
// no-ops. Once the wizard ships, that branch will route into it
// instead.
func newSetupCmd(configPath *string) *cobra.Command {
	var (
		doSkills     bool
		force        bool
		diff         bool
		merge        bool
		checkUpdates bool
		fromDir      string
	)

	cmd := &cobra.Command{
		Use:   "setup [--skills [--diff|--force|--merge|--check-updates|--from-dir <path>]]",
		Short: "Run the OSS setup wizard: install Skills and authenticate sources.",
		Long: "setup provisions the Skills (~/.claude/skills/marunage-*) and the\n" +
			"per-source auth bundles a fresh marunage install needs before its\n" +
			"first autonomous run.\n\n" +
			"This PR implements --skills (Skills install / diff / merge /\n" +
			"check-updates / from-dir). The full interactive wizard and\n" +
			"--source <name> auth flow land in subsequent PRs.\n\n" +
			"--diff prints the diff against the embedded copy without writing.\n" +
			"--force overwrites a hand-edited SKILL.md.\n" +
			"--merge prompts per conflict for overwrite / skip / show-diff.\n" +
			"--check-updates lists embedded vs on-disk versions, no writes.\n" +
			"--from-dir <path> reads from a local directory instead of the\n" +
			"embedded bundle (handy for skill development).",
		// setup is one of the first commands a user hits; suppress
		// cobra's usage banner so an actionable error is not buried
		// under help text.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !doSkills {
				return errSetupNoArm
			}
			return runSkills(cmd, *configPath, runSkillsArgs{
				Force:        force,
				Diff:         diff,
				Merge:        merge,
				CheckUpdates: checkUpdates,
				FromDir:      fromDir,
			})
		},
	}

	cmd.Flags().BoolVar(&doSkills, "skills", false, "Install / diff / update the bundled Skills under ~/.claude/skills/marunage-*.")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite a hand-edited SKILL.md.")
	cmd.Flags().BoolVar(&diff, "diff", false, "Print the diff against the embedded copy without writing.")
	cmd.Flags().BoolVar(&merge, "merge", false, "Prompt per conflict for overwrite / skip / show-diff.")
	cmd.Flags().BoolVar(&checkUpdates, "check-updates", false, "List embedded vs on-disk versions; do not write.")
	cmd.Flags().StringVar(&fromDir, "from-dir", "", "Read Skills from a local directory instead of the embedded bundle.")

	return cmd
}

// errSetupNoArm signals "setup was invoked but no sub-flag selected
// what to do". We surface it as a typed sentinel so a future wizard
// branch can `errors.Is` against it before defaulting to the full
// interactive flow.
var errSetupNoArm = errors.New("setup: specify --skills; the interactive wizard and --from <registry> are not yet implemented")

// runSkillsArgs bundles the Skills-arm flags so the RunE closure stays
// readable. It is package-private — tests drive `Execute` end-to-end.
type runSkillsArgs struct {
	Force        bool
	Diff         bool
	Merge        bool
	CheckUpdates bool
	FromDir      string
}

func runSkills(cmd *cobra.Command, configPath string, args runSkillsArgs) error {
	target, err := skillsTargetDir()
	if err != nil {
		return err
	}

	source, err := resolveSkillSource(args.FromDir)
	if err != nil {
		return err
	}

	// Open the audit writer only for the modes that mutate disk.
	// --diff and --check-updates are pure read operations and would
	// otherwise create an empty logs/ tree on first run.
	var auditor config.Auditor = config.NopAuditor{}
	closeAudit := func() {}
	if !args.Diff && !args.CheckUpdates {
		a, closer, err := openSetupAuditor(configPath)
		if err != nil {
			return err
		}
		auditor = a
		closeAudit = closer
	}
	defer closeAudit()

	res, err := skills.Install(skills.InstallOptions{
		Target:       target,
		Source:       source,
		Force:        args.Force,
		Diff:         args.Diff,
		Merge:        args.Merge,
		CheckUpdates: args.CheckUpdates,
		Out:          cmd.OutOrStdout(),
		In:           activeStdinReader(),
		Auditor:      auditor,
	})
	if err != nil {
		return err
	}

	printSkillsResult(cmd.OutOrStdout(), target, res)
	return nil
}

// openSetupAuditor mirrors openInitAuditor: open the audit writer at
// the documented per-config-path location and hand back a no-arg
// closer. A failure to open is fatal because the "No silent execution"
// invariant explicitly requires setup.skills.* to land on disk.
func openSetupAuditor(configPath string) (config.Auditor, func(), error) {
	auditPath := auditLogPathFor(configPath)
	a, err := logging.NewAuditLog(auditPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open audit log %s: %w", auditPath, err)
	}
	return a, func() { _ = a.Close() }, nil
}

// skillsTargetDir resolves ~/.claude/skills, the documented install
// root. Lifting it out of the cobra closure keeps the env-shim test
// (HOME pointed at a tempdir) ergonomic and lets a Web UI surface
// reuse the same derivation later.
func skillsTargetDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("setup: cannot resolve HOME (%v); set $HOME or pass --from-dir for development", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// resolveSkillSource picks between the //go:embed bundle (default) and
// a user-supplied directory. Returning an `fs.FS` keeps the boundary
// the same in either case so the installer does not need to branch.
func resolveSkillSource(fromDir string) (fs.FS, error) {
	if fromDir == "" {
		return skills.EmbeddedFS(), nil
	}
	info, err := os.Stat(fromDir)
	if err != nil {
		return nil, fmt.Errorf("--from-dir %s: %w", fromDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("--from-dir %s: not a directory", fromDir)
	}
	return os.DirFS(fromDir), nil
}

// printSkillsResult renders the per-bucket summary so a user can see
// at a glance what changed. The format is a stable enough convention
// for users to grep ("Installed:") but is NOT a programmatic interface
// — machine consumers should call internal/skills directly.
//
// Diff / CheckUpdates renders inline in skills.Install via the Out
// writer; the post-write summary therefore only needs to surface the
// install / skip / update buckets.
func printSkillsResult(w io.Writer, target string, res skills.InstallResult) {
	for _, r := range res.Installed {
		fmt.Fprintf(w, "Installed: %s (version %s) -> %s\n", r.Name, r.NewVersion, filepath.Join(target, r.Name))
	}
	for _, r := range res.Updated {
		fmt.Fprintf(w, "Updated:   %s (%s -> %s)\n", r.Name, r.OldVersion, r.NewVersion)
	}
	for _, r := range res.Skipped {
		fmt.Fprintf(w, "Skipped:   %s (already at %s, use --force to overwrite)\n", r.Name, r.NewVersion)
	}
}
