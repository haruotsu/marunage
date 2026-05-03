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

// secretPatterns is the (compiled, ordered) list of regexes Redact walks
// to mask secrets. Order matters: more-specific prefixes (e.g.
// `sk-ant-...`) come before broader ones (`sk-...`) so the longer match
// always wins. Each pattern targets ONE provider; we deliberately avoid
// generic high-entropy detection because UUIDs / commit SHAs / base64
// fixtures would false-positive and obscure real failures.
//
// Pattern shape rationale:
//   - 20+ char tail keeps the regex from matching short, non-secret
//     strings that happen to start with the same prefix.
//   - `[A-Za-z0-9_\-]` is the safe set across all listed providers; any
//     extra character (e.g. ".") would let the pattern run past the
//     secret and into surrounding text.
//
// MAINTAINER NOTE: the matching unit-test cases live in
// redact_test.go; if you add or change a pattern, update both.
var secretPatterns = []*regexp.Regexp{
	// Anthropic API keys: sk-ant-[anything]-[20+ chars]
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`),
	// OpenAI / generic sk- keys (must come AFTER the sk-ant- pattern).
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),
	// GitHub PATs: ghp_, gho_, ghu_, ghs_, ghr_ followed by 20+ chars.
	regexp.MustCompile(`gh[psour]_[A-Za-z0-9]{20,}`),
	// Slack tokens: xoxb-, xoxp-, xoxa-, xoxr-, xoxs-.
	regexp.MustCompile(`xox[bpars]-[A-Za-z0-9\-]{10,}`),
	// Bearer / Authorization header values. We match the value AFTER
	// "Bearer " (case-insensitive) so the header keyword survives in
	// the log line for forensic context.
	regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9_\-\.~+/]+=*`),
}

// Redact returns s with every recognised secret pattern replaced by
// "[REDACTED]". For the Bearer/Authorization pattern the keyword
// itself is preserved so a reviewer can still tell what kind of header
// originally appeared in the failure message.
//
// The function is safe to call on any string and is the canonical
// pre-write sanitiser for anything that lands in judgment_reason,
// audit.log, or daemon.log.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	for i, re := range secretPatterns {
		// Bearer pattern preserves capture group 1 (the header keyword).
		// Other patterns blank the whole match.
		if i == len(secretPatterns)-1 {
			out = re.ReplaceAllString(out, "${1}"+redactionPlaceholder)
			continue
		}
		out = re.ReplaceAllString(out, redactionPlaceholder)
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
