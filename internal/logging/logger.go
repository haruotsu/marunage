package logging

import (
	"fmt"
	"io"
	"log/slog"
)

// Level aliases slog.Level so callers do not need a second import for the
// canonical level constants.
type Level = slog.Level

// Logger aliases slog.Logger so the rest of the binary can depend on this
// package without leaking the slog package into every import list. If we
// later swap the underlying handler the type alias keeps callers stable.
type Logger = slog.Logger

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// NewLogger builds a slog-backed JSON Lines logger. The output format is
// the contract daemon.log readers depend on; switching to text/Pretty here
// would break operators' jq pipelines.
func NewLogger(w io.Writer, level Level) *Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// ParseLevel maps the lower-case strings used in config.toml's
// core.log_level (validated to debug/info/warn/error) to slog levels.
// Unknown inputs return an error so a typo surfaces at startup rather than
// silently downgrading to info.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return LevelInfo, fmt.Errorf("unknown log level %q", s)
}
