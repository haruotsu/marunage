package doctor

// Internal tests for the per-distro classification used by --fix.
// linuxFamilyFromOSRelease is what decides whether a Linux user sees
// "sudo apt install ..." or "sudo dnf install ..." — getting it wrong
// once means every Linux user gets the wrong copy-paste line. Pin the
// well-known os-release fixtures directly.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxFamilyFromOSRelease(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    OSFamily
	}{
		{
			name: "ubuntu",
			content: `NAME="Ubuntu"
ID=ubuntu
ID_LIKE=debian
VERSION_ID="22.04"
`,
			want: OSFamilyDebian,
		},
		{
			name: "debian",
			content: `NAME="Debian GNU/Linux"
ID=debian
VERSION_ID="12"
`,
			want: OSFamilyDebian,
		},
		{
			name: "fedora",
			content: `NAME="Fedora Linux"
ID=fedora
VERSION_ID="40"
`,
			want: OSFamilyFedora,
		},
		{
			name: "centos via id_like",
			content: `NAME="CentOS Stream"
ID="centos"
ID_LIKE="rhel fedora"
`,
			want: OSFamilyFedora,
		},
		{
			name: "rhel",
			content: `NAME="Red Hat Enterprise Linux"
ID="rhel"
ID_LIKE="fedora"
`,
			want: OSFamilyFedora,
		},
		{
			name: "alpine falls back to other",
			content: `NAME="Alpine Linux"
ID=alpine
`,
			want: OSFamilyOther,
		},
		{
			name: "arch falls back to other",
			content: `NAME="Arch Linux"
ID=arch
`,
			want: OSFamilyOther,
		},
		{
			name: "single-quoted id is unquoted",
			content: `ID='ubuntu'
`,
			want: OSFamilyDebian,
		},
		{
			name:    "empty file",
			content: ``,
			want:    OSFamilyOther,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempOSRelease(t, tc.content)
			if got := linuxFamilyFromOSRelease(path); got != tc.want {
				t.Errorf("linuxFamilyFromOSRelease = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestLinuxFamilyFromOSRelease_MissingFile(t *testing.T) {
	// A missing /etc/os-release must not panic; the production code
	// silently falls back to OSFamilyOther so the user still gets
	// generic upstream-URL hints.
	got := linuxFamilyFromOSRelease(filepath.Join(t.TempDir(), "does-not-exist"))
	if got != OSFamilyOther {
		t.Errorf("missing file -> %v; want OSFamilyOther", got)
	}
}

func TestRealOSDetector_GOOSOverride(t *testing.T) {
	// Pin the cross-platform fan-out so the production OSDetector keeps
	// returning OSFamilyDarwin on darwin and OSFamilyOther on windows
	// without having to spin up real hosts.
	cases := []struct {
		goos string
		want OSFamily
	}{
		{"darwin", OSFamilyDarwin},
		{"windows", OSFamilyOther},
		{"freebsd", OSFamilyOther},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			d := RealOSDetector{GOOS: tc.goos}
			if got := d.Family(); got != tc.want {
				t.Errorf("Family(GOOS=%q) = %v; want %v", tc.goos, got, tc.want)
			}
		})
	}
}

func TestRealOSDetector_LinuxConsultsOSReleasePath(t *testing.T) {
	// The Linux branch must read the path the test injects, not the
	// real /etc/os-release on the host. Pin that wiring.
	path := writeTempOSRelease(t, "ID=fedora\n")
	d := RealOSDetector{GOOS: "linux", OSReleasePath: path}
	if got := d.Family(); got != OSFamilyFedora {
		t.Errorf("Family(linux + injected path) = %v; want OSFamilyFedora", got)
	}
}

func writeTempOSRelease(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp os-release: %v", err)
	}
	return path
}
