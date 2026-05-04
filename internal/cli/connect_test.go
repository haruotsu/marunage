package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testConnectorTOML = `[connector]
name = "test-webhook"
type = "discover"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:9999/discover"

[auth]
type = "none"
`

func TestConnect_LocalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	f, err := os.CreateTemp(t.TempDir(), "connector*.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(testConnectorTOML); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"connect", f.Name()}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("connect exit=%d, stderr=%q", code, stderr.String())
	}

	dest := filepath.Join(home, ".marunage", "connectors", "test-webhook", "connector.toml")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("connector.toml not found at %s: %v", dest, err)
	}
	content, _ := os.ReadFile(dest)
	if !strings.Contains(string(content), "test-webhook") {
		t.Errorf("connector.toml does not contain connector name")
	}
}

func TestConnect_URL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/toml")
		_, _ = w.Write([]byte(testConnectorTOML))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"connect", srv.URL}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("connect exit=%d, stderr=%q", code, stderr.String())
	}

	dest := filepath.Join(home, ".marunage", "connectors", "test-webhook", "connector.toml")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("connector.toml not found at %s: %v", dest, err)
	}
}

func TestConnect_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"connect"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for missing argument")
	}
}

func TestConnect_InvalidTOML_URL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not valid toml at all ][[["))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"connect", srv.URL}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for invalid TOML")
	}
}
