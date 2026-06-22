package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/version"
)

// requiredTopLevelSubcommands is the full Phase 1 surface defined in
// docs/requirement.md "コマンド `marunage`". PR-02 wires every entry as a
// stub so `marunage --help` shows the complete UX skeleton even before the
// individual implementations land.
var requiredTopLevelSubcommands = []string{
	"init", "doctor", "setup", "add", "list", "show", "rm", "done", "fail",
	"discover", "dispatch", "run-all", "status", "render", "open", "notify",
	"loop", "daemon", "web", "promote", "reopen", "review", "clean", "export",
	"config",
}

func TestExecute_Help_ListsAllRequiredSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Execute([]string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute --help exit=%d; want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, sub := range requiredTopLevelSubcommands {
		if !strings.Contains(out, sub) {
			t.Errorf("--help output missing subcommand %q\nfull output:\n%s", sub, out)
		}
	}
}

func TestExecute_Version_PrintsBareVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Execute([]string{"--version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute --version exit=%d; want 0; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != version.Version() {
		t.Errorf("--version stdout = %q; want %q", got, version.Version())
	}
}

func TestExecute_Subcommand_HelpAlwaysSucceeds(t *testing.T) {
	for _, sub := range requiredTopLevelSubcommands {
		t.Run(sub+" --help", func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Execute([]string{sub, "--help"}, &stdout, &stderr)

			if code != 0 {
				t.Fatalf("Execute %q --help exit=%d; want 0; stderr=%q", sub, code, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Errorf("Execute %q --help wrote nothing to stdout", sub)
			}
		})
	}
}

func TestExecute_DaemonGroup(t *testing.T) {
	subs := []string{"start", "stop", "status"}

	t.Run("daemon --help lists subcommands", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := Execute([]string{"daemon", "--help"}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("daemon --help exit=%d; want 0; stderr=%q", code, stderr.String())
		}
		for _, s := range subs {
			if !strings.Contains(stdout.String(), s) {
				t.Errorf("daemon --help missing subcommand %q\noutput:\n%s", s, stdout.String())
			}
		}
	})
}

func TestExecute_ConfigGroup(t *testing.T) {
	allSubs := []string{"get", "set", "edit", "wizard"}

	t.Run("config --help lists subcommands", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		code := Execute([]string{"config", "--help"}, &stdout, &stderr)

		if code != 0 {
			t.Fatalf("config --help exit=%d; want 0; stderr=%q", code, stderr.String())
		}
		for _, s := range allSubs {
			if !strings.Contains(stdout.String(), s) {
				t.Errorf("config --help missing subcommand %q\noutput:\n%s", s, stdout.String())
			}
		}
	})
}

func TestExecute_UnknownCommand_NonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Execute([]string{"definitely-not-a-command"}, &stdout, &stderr)

	if code == 0 {
		t.Fatal("unknown command exit=0; want non-zero")
	}
}

func TestExecute_UnknownFlag_NonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Execute([]string{"--definitely-not-a-flag"}, &stdout, &stderr)

	if code == 0 {
		t.Fatal("unknown flag exit=0; want non-zero")
	}
}

func TestExecute_NoArgs_PrintsHelp(t *testing.T) {
	// `marunage` with no args should show the usage so the user sees the
	// command surface immediately. cobra's default behavior already does
	// this; the test pins the contract.
	var stdout, stderr bytes.Buffer

	code := Execute(nil, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute (no args) exit=%d; want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("no-args output missing 'Usage:' section\noutput:\n%s", stdout.String())
	}
}
