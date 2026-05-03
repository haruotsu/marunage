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

// leafStubSubcommands are the subcommands that have no further sub-tree in
// PR-02 and therefore must report "not yet implemented" with a non-zero exit
// code so users discover the missing feature immediately. As individual
// commands ship (config in PR-05, doctor in PR-32, add/list/show in PR-20,
// done/fail/rm/promote/reopen in PR-21, ...) they leave this list.
var leafStubSubcommands = []string{
	"setup",
	"run-all", "open", "notify",
	"loop", "web", "review",
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

func TestExecute_LeafStub_ReportsNotImplemented(t *testing.T) {
	for _, sub := range leafStubSubcommands {
		t.Run(sub, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Execute([]string{sub}, &stdout, &stderr)

			if code == 0 {
				t.Fatalf("Execute %q exit=0; want non-zero for stub", sub)
			}
			if !strings.Contains(stderr.String(), "not yet implemented") {
				t.Errorf("Execute %q stderr=%q; want substring %q", sub, stderr.String(), "not yet implemented")
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

	for _, s := range subs {
		t.Run("daemon "+s, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Execute([]string{"daemon", s}, &stdout, &stderr)

			if code == 0 {
				t.Fatalf("daemon %s exit=0; want non-zero stub", s)
			}
			if !strings.Contains(stderr.String(), "not yet implemented") {
				t.Errorf("daemon %s stderr=%q; want substring %q", s, stderr.String(), "not yet implemented")
			}
		})
	}
}

func TestExecute_ConfigGroup(t *testing.T) {
	allSubs := []string{"get", "set", "edit", "wizard"}
	stillStubbed := []string{"edit", "wizard"}

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

	for _, s := range stillStubbed {
		t.Run("config "+s, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Execute([]string{"config", s}, &stdout, &stderr)

			if code == 0 {
				t.Fatalf("config %s exit=0; want non-zero stub", s)
			}
			if !strings.Contains(stderr.String(), "not yet implemented") {
				t.Errorf("config %s stderr=%q; want substring %q", s, stderr.String(), "not yet implemented")
			}
		})
	}
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
