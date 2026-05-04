package notion

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// memSecrets is the in-memory SecretsStore used by Setup / AuthStatus tests.
// Mirrors the Get/Set surface of internal/secrets.Store closely enough that
// swapping in the real one at the call site is a one-line change.
type memSecrets struct {
	mu sync.Mutex
	kv map[string]string
}

func newMemSecrets() *memSecrets { return &memSecrets{kv: map[string]string{}} }

func (m *memSecrets) Get(name string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[name]
	return v, ok, nil
}

func (m *memSecrets) Set(name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[name] = value
	return nil
}

// TestAuthStatusNotConfiguredWhenNoToken — fresh install path: secrets store
// has no entry for the documented key, so AuthStatus must say "not
// configured" and the CLI prompts for setup.
func TestAuthStatusNotConfiguredWhenNoToken(t *testing.T) {
	t.Parallel()

	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"), WithSecrets(newMemSecrets()))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthNotConfigured)
	}
}

// TestAuthStatusAuthenticatedWhenTokenAndSmokeOk — happy path: token in
// secrets + Users.me probe returns nil → authenticated.
func TestAuthStatusAuthenticatedWhenTokenAndSmokeOk(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	_ = s.Set(defaultSecretName, "secret_abc123")

	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"), WithSecrets(s))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthAuthenticated)
	}
}

// TestAuthStatusRevokedWhenSmokeReturns401 — token persisted but Notion
// rejected it. The CLI surfaces this as "re-run setup with a fresh token".
func TestAuthStatusRevokedWhenSmokeReturns401(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	_ = s.Set(defaultSecretName, "stale_token")
	c := &fakeClient{usersMeErr: ErrUnauthorized}

	p := New(WithClient(c), WithDatabaseID("db"), WithSecrets(s))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthRevoked {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthRevoked)
	}
}

// TestAuthStatusExpiredWhenSmokeReturnsExpired — OAuth-specific: token aged
// past TTL but the underlying integration still exists. CLI surfaces this
// as "refresh, do not re-grant".
func TestAuthStatusExpiredWhenSmokeReturnsExpired(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	_ = s.Set(defaultSecretName, "expired_token")
	c := &fakeClient{usersMeErr: ErrTokenExpired}

	p := New(WithClient(c), WithDatabaseID("db"), WithSecrets(s))
	got, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthExpired {
		t.Errorf("AuthStatus = %q, want %q", got, source.AuthExpired)
	}
}

// TestAuthStatusWithoutSecretsReturnsTyped — refusing to silently report
// "authenticated" when there is no place to look up the token. Loud failure
// catches a misconfigured wiring at startup rather than at first call.
func TestAuthStatusWithoutSecretsReturnsTyped(t *testing.T) {
	t.Parallel()

	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"))
	_, err := p.AuthStatus(context.Background())
	if !errors.Is(err, ErrNoSecretsConfigured) {
		t.Fatalf("err = %v, want ErrNoSecretsConfigured", err)
	}
}

// TestSetupNonInteractiveWritesToken — the documented headless path: a
// TokenProvider returns the token (CI / Docker setup), and Setup persists
// it under the documented key. No prompt is reached.
func TestSetupNonInteractiveWritesToken(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	provider := func(_ context.Context, _ SetupOpts) (string, error) {
		return "secret_from_env", nil
	}
	p := New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithSecrets(s),
		WithTokenProvider(provider),
	)
	if err := p.Setup(context.Background(), SetupOpts{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	got, _, _ := s.Get(defaultSecretName)
	if got != "secret_from_env" {
		t.Errorf("token persisted = %q", got)
	}
}

// TestSetupRejectsEmptyToken — TokenProvider returning "" is the documented
// "user did not supply a token" signal. The CLI surfaces this as a hard
// error so the user does not silently end up with AuthNotConfigured.
func TestSetupRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	provider := func(_ context.Context, _ SetupOpts) (string, error) { return "", nil }
	p := New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithSecrets(s),
		WithTokenProvider(provider),
	)
	err := p.Setup(context.Background(), SetupOpts{NonInteractive: true})
	if !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("err = %v, want ErrTokenRequired", err)
	}
}

// TestSetupWithoutSecretsReturnsTyped — same loud-failure rationale as
// AuthStatus. There is no place to write the token, so Setup must refuse
// rather than silently no-op.
func TestSetupWithoutSecretsReturnsTyped(t *testing.T) {
	t.Parallel()

	provider := func(_ context.Context, _ SetupOpts) (string, error) { return "x", nil }
	p := New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithTokenProvider(provider),
	)
	err := p.Setup(context.Background(), SetupOpts{NonInteractive: true})
	if !errors.Is(err, ErrNoSecretsConfigured) {
		t.Fatalf("err = %v, want ErrNoSecretsConfigured", err)
	}
}

// TestSetupDefaultProviderReadsEnv — when the caller does not wire
// WithTokenProvider, Setup must fall back to the documented headless
// path (MARUNAGE_NOTION_TOKEN env var). t.Setenv restores the original
// value automatically so the test does not leak across packages.
func TestSetupDefaultProviderReadsEnv(t *testing.T) {
	t.Setenv(defaultTokenEnv, "secret_from_env_var")
	s := newMemSecrets()
	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"), WithSecrets(s))
	if err := p.Setup(context.Background(), SetupOpts{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	got, _, _ := s.Get(defaultSecretName)
	if got != "secret_from_env_var" {
		t.Errorf("token persisted = %q", got)
	}
}

// TestSetupDefaultProviderEmptyEnvIsTokenRequired — the env var being unset
// is the documented "user did not supply a token" signal; Setup must
// surface it as ErrTokenRequired so the CLI can prompt precisely.
func TestSetupDefaultProviderEmptyEnvIsTokenRequired(t *testing.T) {
	t.Setenv(defaultTokenEnv, "")
	s := newMemSecrets()
	p := New(WithClient(&fakeClient{}), WithDatabaseID("db"), WithSecrets(s))
	err := p.Setup(context.Background(), SetupOpts{NonInteractive: true})
	if !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("err = %v, want ErrTokenRequired", err)
	}
}

// TestSetupRespectsWithSecretName — overriding the secret key must persist
// the token under the new name (and AuthStatus must read from there too).
// The default test elsewhere covers "notion:token"; this pins the override
// path so a deployment using a custom secrets layout does not silently
// fall back to the default key.
func TestSetupRespectsWithSecretName(t *testing.T) {
	t.Parallel()

	s := newMemSecrets()
	provider := func(_ context.Context, _ SetupOpts) (string, error) { return "tok", nil }
	p := New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithSecrets(s),
		WithSecretName("marunage/notion"),
		WithTokenProvider(provider),
	)
	if err := p.Setup(context.Background(), SetupOpts{NonInteractive: true}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if v, _, _ := s.Get("marunage/notion"); v != "tok" {
		t.Errorf("token at custom key = %q", v)
	}
	if _, ok, _ := s.Get(defaultSecretName); ok {
		t.Errorf("token also written to default key — leak across keys")
	}
}

// TestSetupPropagatesProviderError — a TokenProvider that errors (e.g. a
// terminal prompt cancelled) must surface the error verbatim so the CLI
// can distinguish "user aborted" from "wrote token successfully".
func TestSetupPropagatesProviderError(t *testing.T) {
	t.Parallel()

	want := errors.New("aborted")
	provider := func(_ context.Context, _ SetupOpts) (string, error) { return "", want }
	p := New(
		WithClient(&fakeClient{}),
		WithDatabaseID("db"),
		WithSecrets(newMemSecrets()),
		WithTokenProvider(provider),
	)
	err := p.Setup(context.Background(), SetupOpts{NonInteractive: true})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
