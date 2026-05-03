package logging

import (
	"context"
	"log/slog"
	"regexp"
)

// redactionPlaceholder is the literal string substituted in for any
// matched secret. Centralised so callers ("does this log line contain a
// redacted token?") can match on it without typing the magic string in
// multiple places.
const redactionPlaceholder = "[REDACTED]"

// secretPattern bundles a compiled regex with whether the regex
// preserves a leading capture group (so a header / query keyword like
// "Bearer " or "password=" survives in the masked output for forensic
// context).
type secretPattern struct {
	re        *regexp.Regexp
	keepGroup bool
}

// secretPatterns is the ordered list of regexes Redact walks to mask
// secrets. Order matters: more-specific prefixes (`sk-ant-...`) come
// before broader ones (`sk-...`) so the longer match always wins.
//
// Pattern shape rationale:
//   - 20+ char tail keeps the regex from matching short, non-secret
//     strings that happen to start with the same prefix.
//   - `[A-Za-z0-9_\-]` is the safe set across all listed providers;
//     any extra character (e.g. ".") would let the pattern run past
//     the secret and into surrounding text.
//
// MAINTAINER NOTE: matching unit-test cases live in redact_test.go;
// if you add or change a pattern, update both.
var secretPatterns = []secretPattern{
	// Anthropic API keys: sk-ant-[anything]-[20+ chars]
	{re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	// OpenAI / generic sk- keys (MUST come AFTER the sk-ant- pattern).
	{re: regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`)},
	// GitHub PATs: ghp_, gho_, ghu_, ghs_, ghr_ followed by 20+ chars.
	{re: regexp.MustCompile(`gh[psour]_[A-Za-z0-9]{20,}`)},
	// Slack tokens: xoxb-, xoxp-, xoxa-, xoxr-, xoxs-.
	{re: regexp.MustCompile(`xox[bpars]-[A-Za-z0-9\-]{10,}`)},
	// Bearer header value (case-insensitive); keep the keyword.
	{re: regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9_\-\.~+/]+=*`), keepGroup: true},
	// Basic auth header value (base64 user:pass); keep the keyword.
	{re: regexp.MustCompile(`(?i)(Basic\s+)[A-Za-z0-9+/]+=*`), keepGroup: true},
	// URL query string secrets: ?password= / ?passwd= / ?api_key= /
	// ?apikey= / ?token= / ?access_token= / ?secret=. Match key=value
	// up to the next `&` or whitespace. The key prefix is preserved
	// so a reviewer can tell which parameter leaked.
	{re: regexp.MustCompile(`(?i)((?:password|passwd|api[_-]?key|token|access[_-]?token|secret)=)[^&\s]+`), keepGroup: true},
}

// Redact returns s with every recognised secret pattern replaced by
// "[REDACTED]". For the Bearer/Authorization pattern the keyword
// itself is preserved so a reviewer can still tell what kind of header
// originally appeared in the failure message.
//
// The function is safe to call on any string and is the canonical
// pre-write sanitiser for anything that lands in judgment_reason,
// audit.log, or daemon.log.
//
// COVERAGE LIMITS — patterns this function does NOT yet match:
//   - AWS access keys (AKIA[A-Z0-9]{16})
//   - Google API keys (AIza[A-Za-z0-9_-]{35})
//   - Stripe live keys (sk_live_…)
//   - Raw JWTs not behind a Bearer keyword (eyJ…)
//   - HTTP form-encoded POST bodies (key=value pairs in body, not URL)
//
// The list is intentionally conservative — adding generic high-entropy
// detection would mask UUIDs, commit SHAs, and base64 fixtures (high
// false-positive rate that would obscure real failures). Add a new
// pattern to secretPatterns when marunage actually integrates with the
// corresponding provider so the regex is grounded in a real call site.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, p := range secretPatterns {
		if p.keepGroup {
			out = p.re.ReplaceAllString(out, "${1}"+redactionPlaceholder)
			continue
		}
		out = p.re.ReplaceAllString(out, redactionPlaceholder)
	}
	return out
}

// redactingHandler wraps a slog.Handler so the message AND every string
// attribute pass through Redact before serialisation. Non-string
// attributes (int, bool, time, etc.) are passed through unchanged
// because secrets in marunage's call sites only ever appear as strings.
type redactingHandler struct {
	inner slog.Handler
}

// WithRedaction wraps base so every line it emits has Redact applied to
// the message and to every string attribute. Used by the daemon main()
// to harden the global logger; tests use it to pin the wrap behaviour.
//
// Wrapping the LOGGER (not the HANDLER directly) is the public surface
// because slog.New takes a Handler and the convention across the
// codebase is "callers receive a *slog.Logger".
func WithRedaction(base *Logger) *Logger {
	return slog.New(&redactingHandler{inner: base.Handler()})
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	scrubbed := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		scrubbed.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, scrubbed)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(cleaned)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr applies Redact to string-valued attributes; group attrs
// recurse so nested structures are scrubbed too.
func redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindGroup:
		members := a.Value.Group()
		cleaned := make([]any, 0, len(members)*2)
		for _, m := range members {
			c := redactAttr(m)
			cleaned = append(cleaned, c.Key, c.Value)
		}
		return slog.Group(a.Key, cleaned...)
	default:
		return a
	}
}
