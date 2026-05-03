package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/initialize"
	"github.com/haruotsu/marunage/internal/logging"
)

// initModeChoices is the ordered prompt the user sees, mirroring
// docs/requirement.md 247-251. The order is the contract: the slot a
// number maps to (1=bypass, 2=default, ...) appears in user docs and
// support transcripts, so swapping rows would silently invalidate them.
var initModeChoices = []struct {
	mode string
	desc string
}{
	{"bypass", "全ツールを自動許可。サンドボックス向け"},
	{"default", "ツール実行ごとに確認。許可リスト併用推奨"},
	{"acceptEdits", "ファイル編集のみ自動許可"},
	{"plan", "実行せず計画のみ"},
	{"custom", "後で claude_command を直接編集"},
}

// newInitCmd builds the `marunage init` subcommand. It owns three
// responsibilities the OSS first-run UX requires:
//
//  1. Make sure ~/.marunage/ and its subdirs exist (delegated to
//     internal/initialize so the side effects stay testable).
//  2. Prompt the user for a permission mode, or honour --mode when
//     --non-interactive is set so init can be embedded in setup scripts.
//  3. Print the next-step guidance (`marunage doctor` then `marunage setup
//     --skills`) so a brand-new user is not left wondering what to do
//     after the file appears on disk.
func newInitCmd(configPath *string) *cobra.Command {
	var (
		nonInteractive bool
		mode           string
	)

	cmd := &cobra.Command{
		Use:   "init [--non-interactive] [--mode <bypass|default|acceptEdits|plan|custom>]",
		Short: "Initialize ~/.marunage/, the SQLite store, and select a permission mode.",
		Long: "init is the OSS first-run experience: it creates ~/.marunage/, writes\n" +
			"a default config.toml the first time, prompts for the permission mode\n" +
			"that drives Claude Code's auto-execution, and points you at the next\n" +
			"two commands (doctor, setup --skills).\n\n" +
			"The command is idempotent — re-running it on an already-initialised\n" +
			"home does not overwrite config.toml. Use --non-interactive (with an\n" +
			"optional --mode) to embed it in scripts.",
		// init is a first-run command; suppress cobra's "use --help" usage
		// banner on errors so a typo does not bury the actionable message.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := homeFromConfigPath(*configPath)
			if err != nil {
				return err
			}

			chosen := mode
			if !nonInteractive {
				picked, err := promptForMode(activeStdinReader(), cmd.OutOrStdout(), mode)
				if err != nil {
					return err
				}
				chosen = picked
			}

			// Validate mode BEFORE opening the audit writer so a typo'd
			// flag never leaves a stale logs/ tree behind (mirrors the
			// comment in newConfigCmd). initialize.Run also validates,
			// but by then logging.NewAuditLog has already MkdirAll'd
			// logs/ and created an empty audit.log.
			resolved, err := initialize.ResolveMode(chosen)
			if err != nil {
				return err
			}

			auditor, closeAudit, err := openInitAuditor(*configPath)
			if err != nil {
				return err
			}
			defer closeAudit()

			res, err := initialize.Run(initialize.Options{
				Home:    home,
				Mode:    resolved,
				Auditor: auditor,
			})
			if err != nil {
				return err
			}
			printInitResult(cmd.OutOrStdout(), res, resolved)
			return nil
		},
	}

	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Skip the prompt; use --mode (defaults to bypass).")
	cmd.Flags().StringVar(&mode, "mode", "", "Permission mode: bypass | default | acceptEdits | plan | custom.")
	return cmd
}

// homeFromConfigPath turns the active --config path into the implicit
// home directory init provisions. The contract is "--config must point
// at <home>/.marunage/config.toml" — anything else gets rejected so a
// user testing init with a throwaway path cannot accidentally mutate
// their real ~/.marunage.
func homeFromConfigPath(p string) (string, error) {
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return home, nil
	}
	dir := filepath.Dir(p)
	if filepath.Base(dir) != ".marunage" {
		return "", fmt.Errorf("--config %q: must point inside a .marunage/ directory (e.g. %s/.marunage/config.toml)", p, filepath.Dir(p))
	}
	return filepath.Dir(dir), nil
}

// promptForMode renders the documented numeric menu, reads stdin until a
// valid choice arrives (or stdin closes), and returns the canonical mode
// string. An explicit --mode supplied alongside the prompt short-circuits
// the read; that path lets `init --mode default` skip the menu without
// also requiring --non-interactive.
func promptForMode(in io.Reader, out io.Writer, prefilled string) (string, error) {
	if prefilled != "" {
		return prefilled, nil
	}

	fmt.Fprintln(out, "本OSSは Claude Code を起動してタスクを自律実行します。")
	fmt.Fprintln(out, "権限モードを選択してください:")
	fmt.Fprintln(out)
	for i, c := range initModeChoices {
		marker := "  "
		if i == 0 {
			marker = "* "
		}
		fmt.Fprintf(out, "%s%d) %-12s -- %s\n", marker, i+1, c.mode, c.desc)
	}
	fmt.Fprintln(out)

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "選択 [1]: ")
		if !scanner.Scan() {
			// EOF on stdin (e.g. closed pipe) — accept the documented
			// default rather than fail. Embedding init in `yes "" |
			// marunage init` should not need --non-interactive.
			if err := scanner.Err(); err != nil {
				return "", fmt.Errorf("read prompt: %w", err)
			}
			return initModeChoices[0].mode, nil
		}
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			return initModeChoices[0].mode, nil
		}
		n, err := strconv.Atoi(raw)
		if err == nil && n >= 1 && n <= len(initModeChoices) {
			return initModeChoices[n-1].mode, nil
		}
		fmt.Fprintf(out, "invalid choice %q; pick 1-%d\n", raw, len(initModeChoices))
	}
}

// openInitAuditor mirrors the pattern in newConfigCmd: open the audit
// writer at the documented per-config-path location and hand back a
// no-arg closer the caller defers. A failure to open the writer is
// fatal for init because the "No silent execution" invariant explicitly
// requires init.create / init.skip to land on disk.
func openInitAuditor(configPath string) (config.Auditor, func(), error) {
	auditPath := auditLogPathFor(configPath)
	a, err := logging.NewAuditLog(auditPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open audit log %s: %w", auditPath, err)
	}
	return a, func() { _ = a.Close() }, nil
}

// printInitResult renders the post-init human message: a one-line summary
// of what happened plus the next-step guidance the OSS UX promises.
func printInitResult(w io.Writer, res initialize.Result, mode string) {
	if res.ConfigCreated {
		fmt.Fprintf(w, "Initialized marunage at %s (permission_mode=%s).\n", res.ConfigPath, mode)
	} else {
		fmt.Fprintf(w, "marunage already initialized at %s; leaving the existing config untouched.\n", res.ConfigPath)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. marunage doctor              # check that claude / cmux / sqlite3 / ... are usable")
	fmt.Fprintln(w, "  2. marunage setup --skills      # install the bundled triage / execute / reflect skills")
}
