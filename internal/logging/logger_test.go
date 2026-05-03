package logging_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/logging"
)

func decodeJSONLines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(b)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

// TestLoggerWritesJSONLines pins the on-disk format used by daemon.log:
// every level method (Info here) emits exactly one JSON object on its own
// line, with the canonical slog field names so external tooling (jq,
// grafana-loki ingest, etc.) can parse it without a custom schema.
func TestLoggerWritesJSONLines(t *testing.T) {
	var buf bytes.Buffer
	l := logging.NewLogger(&buf, logging.LevelInfo)

	l.Info("dispatched", "task_id", 42, "ws", "workspace:7")

	lines := decodeJSONLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("lines = %d; want 1", len(lines))
	}
	got := lines[0]
	if got["msg"] != "dispatched" {
		t.Errorf("msg = %v; want %q", got["msg"], "dispatched")
	}
	if got["level"] != "INFO" {
		t.Errorf("level = %v; want %q", got["level"], "INFO")
	}
	if _, ok := got["time"]; !ok {
		t.Errorf("time field missing; got %v", got)
	}
	if got["task_id"].(float64) != 42 {
		t.Errorf("task_id = %v; want 42", got["task_id"])
	}
	if got["ws"] != "workspace:7" {
		t.Errorf("ws = %v; want %q", got["ws"], "workspace:7")
	}
}

// TestLoggerRespectsLevel: Debug calls below the configured threshold must
// be dropped entirely (no line written). This is the cost-control mechanism
// for daemon.log — debug logging is intended for opt-in troubleshooting.
func TestLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := logging.NewLogger(&buf, logging.LevelInfo)

	l.Debug("noisy")
	l.Info("kept")

	lines := decodeJSONLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("lines = %d; want 1 (debug filtered)", len(lines))
	}
	if lines[0]["msg"] != "kept" {
		t.Errorf("kept-line msg = %v; want %q", lines[0]["msg"], "kept")
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want logging.Level
	}{
		{"debug", logging.LevelDebug},
		{"info", logging.LevelInfo},
		{"warn", logging.LevelWarn},
		{"error", logging.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := logging.ParseLevel(tc.in)
			if err != nil {
				t.Fatalf("ParseLevel(%q) = %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseLevel(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseLevelRejectsUnknown(t *testing.T) {
	_, err := logging.ParseLevel("trace")
	if err == nil {
		t.Fatal("ParseLevel(trace) = nil; want error")
	}
	if !strings.Contains(err.Error(), "trace") {
		t.Errorf("err = %v; want mention of input value", err)
	}
}
