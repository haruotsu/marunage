package logging_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/logging"
)

// TestRedactMasksKnownTokens pins the patterns the dispatcher relies on
// to keep secrets out of judgment_reason / audit.log / daemon.log. The
// list mirrors the providers marunage actually integrates with (Anthropic
// API, GitHub PAT, Slack tokens). Generic high-entropy detection is
// deliberately NOT included because the false-positive rate (UUIDs,
// commit SHAs, base64 fixtures) would obscure real failures.
func TestRedactMasksKnownTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// secret is the substring that MUST be gone after redaction;
		// keep is a substring that MUST still be present (to verify we
		// don't blank the whole log line).
		secret string
		keep   string
	}{
		{
			name:   "anthropic-api-key",
			in:     "error: token sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_=ABC leaked",
			secret: "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
			keep:   "error: token",
		},
		{
			name:   "openai-style-key",
			in:     "leaked sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789 in body",
			secret: "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
			keep:   "leaked",
		},
		{
			name:   "github-pat-classic",
			in:     "remote: error: ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567 expired",
			secret: "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567",
			keep:   "expired",
		},
		{
			name:   "github-pat-oauth",
			in:     "Authorization: token gho_AbCdEfGhIjKlMnOpQrStUvWxYz01234567",
			secret: "gho_AbCdEfGhIjKlMnOpQrStUvWxYz01234567",
			keep:   "Authorization",
		},
		{
			name:   "github-pat-server",
			in:     "ghs_AbCdEfGhIjKlMnOpQrStUvWxYz01234567 returned 401",
			secret: "ghs_AbCdEfGhIjKlMnOpQrStUvWxYz01234567",
			keep:   "returned 401",
		},
		{
			name:   "slack-bot-token",
			in:     "msg post failed for xoxb-12345-67890-AbCdEfGhIjKlMnOpQrStUvWx",
			secret: "xoxb-12345-67890-AbCdEfGhIjKlMnOpQrStUvWx",
			keep:   "msg post failed",
		},
		{
			name:   "slack-user-token",
			in:     "auth: xoxp-12345-67890-12345-AbCdEfGhIjKlMnOpQrStUv",
			secret: "xoxp-12345-67890-12345-AbCdEfGhIjKlMnOpQrStUv",
			keep:   "auth",
		},
		{
			name:   "bearer-header",
			in:     "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
			secret: "eyJhbGciOiJIUzI1NiJ9.payload.sig",
			keep:   "Authorization: Bearer",
		},
		{
			// HTTP Basic auth: the base64 blob carries user:pass and must
			// be masked. The Basic keyword survives so a reviewer still
			// sees the failure originated at an Authorization header.
			name:   "basic-auth-header",
			in:     "Authorization: Basic dXNlcjpzZWNyZXRwYXNzd29yZDEyMw==",
			secret: "dXNlcjpzZWNyZXRwYXNzd29yZDEyMw==",
			keep:   "Authorization: Basic",
		},
		{
			// Tokens / passwords smuggled through a URL query string are a
			// classic leak path. Mask the value while keeping the key
			// label so a reviewer can still grep the failure context.
			name:   "url-password-query",
			in:     "GET https://api.example.com/v1/x?password=hunter2supersecretvalue&debug=1 -> 401",
			secret: "hunter2supersecretvalue",
			keep:   "GET https://api.example.com",
		},
		{
			name:   "url-api-key-query",
			in:     "GET https://api.example.com/?api_key=abcdef0123456789xyz -> 200",
			secret: "abcdef0123456789xyz",
			keep:   "GET https://api.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := logging.Redact(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Errorf("Redact left secret in place:\n input:  %q\n output: %q", tc.in, got)
			}
			if !strings.Contains(got, tc.keep) {
				t.Errorf("Redact dropped non-secret context %q:\n input:  %q\n output: %q", tc.keep, tc.in, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("Redact did not insert [REDACTED] marker:\n input:  %q\n output: %q", tc.in, got)
			}
		})
	}
}

// TestRedactLeavesPlainTextAlone: a typical dispatcher reason
// ("dispatch: WaitReady failed: cmux: timeout after 60s") contains
// no secrets and must not be mangled (we'd lose forensic value).
func TestRedactLeavesPlainTextAlone(t *testing.T) {
	cases := []string{
		"",
		"dispatch: WaitReady failed: cmux: timeout after 60s",
		"task#42 status=failed reason=lock_key resolve failed: invalid regex",
		"workspace:1234 created at 2026-05-03T12:00:00Z",
	}
	for _, in := range cases {
		got := logging.Redact(in)
		if got != in {
			t.Errorf("Redact(%q) = %q; want unchanged", in, got)
		}
	}
}

// TestRedactingHandlerMasksMessageAndAttrs: when the daemon logger is
// wrapped, both the log message itself AND any attribute value pass
// through Redact before serialisation. Without this, a slog call site
// that interpolates a secret into the message string still leaks.
func TestRedactingHandlerMasksMessageAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := logging.NewLogger(&buf, logging.LevelInfo)
	l := logging.WithRedaction(base)

	l.Info(
		"call failed for sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"upstream", "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567",
		"task_id", 42,
	)

	out := buf.String()
	if strings.Contains(out, "sk-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789") {
		t.Errorf("API key leaked in log message: %s", out)
	}
	if strings.Contains(out, "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz01234567") {
		t.Errorf("PAT leaked in log attribute: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("Expected [REDACTED] marker in output: %s", out)
	}
	// The non-secret attribute survives.
	if !strings.Contains(out, `"task_id":42`) {
		t.Errorf("non-secret attribute dropped: %s", out)
	}
}
