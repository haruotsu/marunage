package doctor

// Internal tests for FileSecretsProbe — the production implementation
// that decides which backends doctor reports as "available". The
// 0700-exact-match contract on the file backend exists to prevent
// silently exposing plaintext tokens to other local users; without
// direct tests a future relaxation could land unnoticed.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileSecretsProbe_AgeFilePresent(t *testing.T) {
	home := t.TempDir()
	mkMarunageDir(t, home)
	writeFile(t, filepath.Join(home, ".marunage", "secrets.age"), "")

	p := FileSecretsProbe{HomeDir: home, GOOS: "linux"}
	got := p.AvailableBackends()
	if !contains(got, "age") {
		t.Errorf("expected 'age' in backends; got %v", got)
	}
}

func TestFileSecretsProbe_FileBackendNeedsExact0700(t *testing.T) {
	home := t.TempDir()
	mkMarunageDir(t, home)
	secrets := filepath.Join(home, ".marunage", "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}

	p := FileSecretsProbe{HomeDir: home, GOOS: "linux"}
	if !contains(p.AvailableBackends(), "file") {
		t.Errorf("0700 secrets dir should advertise 'file'; got %v", p.AvailableBackends())
	}
}

func TestFileSecretsProbe_FileBackendRejectsLooseMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits do not apply on windows")
	}
	home := t.TempDir()
	mkMarunageDir(t, home)
	secrets := filepath.Join(home, ".marunage", "secrets")
	if err := os.Mkdir(secrets, 0o755); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	// Force the mode in case umask narrowed it.
	if err := os.Chmod(secrets, 0o755); err != nil {
		t.Fatalf("chmod secrets: %v", err)
	}

	p := FileSecretsProbe{HomeDir: home, GOOS: "linux"}
	got := p.AvailableBackends()
	if contains(got, "file") {
		t.Errorf("0755 secrets dir must NOT advertise 'file' (would leak tokens); got %v", got)
	}
}

func TestFileSecretsProbe_AgeNotPresent(t *testing.T) {
	home := t.TempDir()
	mkMarunageDir(t, home)

	p := FileSecretsProbe{HomeDir: home, GOOS: "linux"}
	if contains(p.AvailableBackends(), "age") {
		t.Errorf("no secrets.age yet 'age' reported; got %v", p.AvailableBackends())
	}
}

func TestFileSecretsProbe_PassOnPATH(t *testing.T) {
	home := t.TempDir()
	mkMarunageDir(t, home)

	p := FileSecretsProbe{
		HomeDir: home,
		GOOS:    "linux",
		Runner: fakeRunner{
			present:  map[string]string{"pass": "/usr/bin/pass"},
			versions: map[string]string{},
		},
	}
	if !contains(p.AvailableBackends(), "pass") {
		t.Errorf("pass on PATH should advertise 'pass'; got %v", p.AvailableBackends())
	}
}

func TestFileSecretsProbe_KeyringHeuristic(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		runner Runner
		envBus string
		want   bool
	}{
		{"darwin always", "darwin", nil, "", true},
		{"windows always", "windows", nil, "", true},
		{
			name:   "linux secret-tool present",
			goos:   "linux",
			runner: fakeRunner{present: map[string]string{"secret-tool": "/usr/bin/secret-tool"}},
			want:   true,
		},
		{
			name:   "linux gnome-keyring-daemon present",
			goos:   "linux",
			runner: fakeRunner{present: map[string]string{"gnome-keyring-daemon": "/usr/bin/gnome-keyring-daemon"}},
			want:   true,
		},
		{
			name:   "linux nothing -> not available",
			goos:   "linux",
			runner: fakeRunner{present: map[string]string{}},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Isolate DBUS env so the test is deterministic on
			// hosts with a real session bus.
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", tc.envBus)
			got := keyringLikelyAvailable(tc.runner, tc.goos)
			if got != tc.want {
				t.Errorf("keyringLikelyAvailable(goos=%q) = %v; want %v", tc.goos, got, tc.want)
			}
		})
	}
}

func TestFileSecretsProbe_DeterministicOrder(t *testing.T) {
	// JSON consumers (Web UI, CI) read `secrets.version` (the joined
	// backend list) verbatim. Pin the iteration order so a future
	// map-iteration refactor does not flip the output between runs.
	home := t.TempDir()
	mkMarunageDir(t, home)
	writeFile(t, filepath.Join(home, ".marunage", "secrets.age"), "")
	if err := os.Mkdir(filepath.Join(home, ".marunage", "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	p := FileSecretsProbe{
		HomeDir: home,
		GOOS:    "darwin",
		Runner: fakeRunner{
			present: map[string]string{"pass": "/usr/bin/pass"},
		},
	}
	first := p.AvailableBackends()
	for i := 0; i < 10; i++ {
		got := p.AvailableBackends()
		if !equalSlices(first, got) {
			t.Fatalf("non-deterministic order:\n  run0=%v\n  run%d=%v", first, i, got)
		}
	}
	// Documented order: filesystem-discovered first, then PATH, then heuristic.
	want := []string{"age", "file", "pass", "keyring"}
	if !equalSlices(first, want) {
		t.Errorf("order = %v; want %v", first, want)
	}
}

// helpers --------------------------------------------------------------

func mkMarunageDir(t *testing.T, home string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(home, ".marunage"), 0o700); err != nil {
		t.Fatalf("mkdir .marunage: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
