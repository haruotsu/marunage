package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRegistryServer wires a tiny in-memory registry: an index, one
// per-skill manifest, and one tarball body. Centralising the fixture
// keeps each test focused on the behaviour under exercise.
func newRegistryServer(t *testing.T, body []byte, sum string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version": 1,
			"skills": [
				{"name": "marunage-source-jira", "latest": "0.1.0", "description": "Jira"}
			]
		}`))
	})
	mux.HandleFunc("/skills/marunage-source-jira/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"schema_version": 1,
			"name": "marunage-source-jira",
			"versions": [
				{"version": "0.1.0", "tarball_url": "TARBALL_URL", "sha256": "SHA"}
			]
		}`))
	})
	mux.HandleFunc("/skills/marunage-source-jira/0.1.0.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// Patch the placeholder URL inside the manifest after we know
	// the test server's address.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_ = sum // not used here; see Test_FetchTarball_VerifiesSHA256
	return srv
}

// TestClient_FetchIndex_HappyPath pins that the client speaks the
// documented /index.json URL and parses the response.
func TestClient_FetchIndex_HappyPath(t *testing.T) {
	srv := newRegistryServer(t, nil, "")
	c := &Client{BaseURL: srv.URL}

	idx, err := c.FetchIndex(context.Background())
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if len(idx.Skills) != 1 || idx.Skills[0].Name != "marunage-source-jira" {
		t.Errorf("Index = %+v; want one entry for marunage-source-jira", idx)
	}
}

// TestClient_FetchManifest_HappyPath pins the per-skill URL.
func TestClient_FetchManifest_HappyPath(t *testing.T) {
	srv := newRegistryServer(t, nil, "")
	c := &Client{BaseURL: srv.URL}

	m, err := c.FetchManifest(context.Background(), "marunage-source-jira")
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.Name != "marunage-source-jira" {
		t.Errorf("Name = %q", m.Name)
	}
}

// TestClient_FetchManifest_UpstreamError pins the typed sentinel for
// non-2xx responses so the CLI can distinguish "registry said no"
// from a transport failure.
func TestClient_FetchManifest_UpstreamError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/skills/missing/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL}
	_, err := c.FetchManifest(context.Background(), "missing")
	if !errors.Is(err, ErrUpstream) {
		t.Errorf("err = %v; want errors.Is(_, ErrUpstream)", err)
	}
}

// TestClient_FetchTarball_VerifiesSHA256 pins the integrity contract:
// the digest must match. We compute the digest from the served body
// so the assertion is honest about what the client should compare
// against.
func TestClient_FetchTarball_VerifiesSHA256(t *testing.T) {
	body := []byte("hello tarball body")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/t.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL}
	got, err := c.FetchTarball(context.Background(), Version{
		Version:    "0.1.0",
		TarballURL: srv.URL + "/t.tar.gz",
		SHA256:     hexSum,
	})
	if err != nil {
		t.Fatalf("FetchTarball: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("body mismatch: got %q want %q", got, body)
	}
}

// TestClient_FetchTarball_DigestMismatchAborts pins the integrity
// fail-stop: if the body's digest does not match the manifest, the
// client must NOT return the body.
func TestClient_FetchTarball_DigestMismatchAborts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/t.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("attacker swapped this"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL}
	body, err := c.FetchTarball(context.Background(), Version{
		Version:    "0.1.0",
		TarballURL: srv.URL + "/t.tar.gz",
		SHA256:     "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("err = %v; want errors.Is(_, ErrIntegrity)", err)
	}
	if body != nil {
		t.Errorf("body should be nil on integrity failure; got %d bytes", len(body))
	}
}

// TestClient_RejectsInsecureScheme pins the file:// / ftp:// guard.
func TestClient_RejectsInsecureScheme(t *testing.T) {
	c := &Client{BaseURL: "file:///etc/passwd"}
	_, err := c.FetchIndex(context.Background())
	if !errors.Is(err, ErrInsecureRegistry) {
		t.Errorf("err = %v; want errors.Is(_, ErrInsecureRegistry)", err)
	}
}

// TestClient_RejectsOversizedBody pins the slow-loris / DoS guard:
// MaxBodyBytes truncates and surfaces an error rather than letting a
// publisher exhaust the CLI's memory.
func TestClient_RejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{BaseURL: srv.URL, MaxBodyBytes: 10}
	_, err := c.FetchIndex(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("err = %v; want exceeded-bytes error", err)
	}
}

// TestClient_FetchManifest_RejectsEmptyName pins the input validation
// branch — a typo at the CLI layer must not produce a request to
// `<base>/skills//manifest.json`.
func TestClient_FetchManifest_RejectsEmptyName(t *testing.T) {
	c := &Client{BaseURL: "https://example"}
	_, err := c.FetchManifest(context.Background(), "")
	if err == nil {
		t.Errorf("FetchManifest(\"\") err = nil; want error")
	}
}
