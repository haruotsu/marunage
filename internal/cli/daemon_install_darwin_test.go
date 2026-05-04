package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Darwin-specific installer tests.
//
//  INST-D1. Install writes a plist containing the expected label, exe, config,
//           and log paths.
//  INST-D2. Install is idempotent: calling it twice with identical parameters
//           does not write the file a second time (mtime unchanged).
//  INST-D3. Uninstall removes the plist file.
//  INST-D4. Uninstall is a no-op (no error) when the plist does not exist.

func newTestLaunchAgentInstaller(t *testing.T) (*launchAgentInstaller, string) {
	t.Helper()
	dir := t.TempDir()
	plistPath := filepath.Join(dir, plistLabel+".plist")
	return &launchAgentInstaller{
		plistPath: plistPath,
		runctl:    func(_ ...string) error { return nil },
	}, plistPath
}

// INST-D1

func TestDarwinInstall_WritesPlist(t *testing.T) {
	t.Parallel()
	inst, plistPath := newTestLaunchAgentInstaller(t)

	if err := inst.Install("/usr/local/bin/marunage", "/etc/marunage.toml", "/tmp/daemon.log"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	content, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(content)

	for _, want := range []string{
		plistLabel,
		"/usr/local/bin/marunage",
		"/etc/marunage.toml",
		"/tmp/daemon.log",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist missing %q\nplist:\n%s", want, s)
		}
	}
}

// INST-D2

func TestDarwinInstall_Idempotent(t *testing.T) {
	t.Parallel()
	inst, plistPath := newTestLaunchAgentInstaller(t)

	args := []string{"/usr/local/bin/marunage", "/etc/marunage.toml", "/tmp/daemon.log"}
	if err := inst.Install(args[0], args[1], args[2]); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	info1, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("Stat after first: %v", err)
	}

	if err := inst.Install(args[0], args[1], args[2]); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	info2, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("Stat after second: %v", err)
	}

	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("mtime changed on second Install; file was rewritten unnecessarily")
	}
}

// INST-D3

func TestDarwinUninstall_DeletesPlist(t *testing.T) {
	t.Parallel()
	inst, plistPath := newTestLaunchAgentInstaller(t)

	if err := inst.Install("/bin/marunage", "/etc/marunage.toml", "/tmp/daemon.log"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("plist not created: %v", err)
	}

	if err := inst.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist still exists after Uninstall")
	}
}

// INST-D4

func TestDarwinUninstall_NoopWhenFileAbsent(t *testing.T) {
	t.Parallel()
	inst, _ := newTestLaunchAgentInstaller(t)

	if err := inst.Uninstall(); err != nil {
		t.Fatalf("Uninstall on absent file = %v; want nil", err)
	}
}
