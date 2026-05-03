package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiscoverOnce_Markdown_PrintsJSONArray drives the headline path the
// brief documents:
//
//	marunage discover --once --source markdown --file <path>
//
// Expectations:
//   - exit 0
//   - stdout is a JSON array
//   - each entry carries source / external_id / title / done so PR-71's
//     queue-materialiser can ingest the output without a second parse.
func TestDiscoverOnce_Markdown_PrintsJSONArray(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "todo.md")
	if err := os.WriteFile(path, []byte("- [ ] First task\n- [x] Done task\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"discover", "--once", "--source", "markdown", "--file", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("discover exit=%d, stderr=%q", code, stderr.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\noutput:\n%s", err, stdout.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2:\n%s", len(got), stdout.String())
	}
	wantTitles := []string{"First task", "Done task"}
	for i, want := range wantTitles {
		if got[i]["title"] != want {
			t.Errorf("got[%d].title = %v, want %q", i, got[i]["title"], want)
		}
		if got[i]["source"] != "markdown" {
			t.Errorf("got[%d].source = %v, want markdown", i, got[i]["source"])
		}
		if id, ok := got[i]["external_id"].(string); !ok || id == "" {
			t.Errorf("got[%d].external_id = %v, want non-empty string", i, got[i]["external_id"])
		}
	}
	if got[0]["done"] != false {
		t.Errorf("got[0].done = %v, want false", got[0]["done"])
	}
	if got[1]["done"] != true {
		t.Errorf("got[1].done = %v, want true", got[1]["done"])
	}
}

// TestDiscoverOnce_UnknownSource_Errors checks the failure path: a source
// name not in the registry must produce a non-zero exit and a stderr message
// pointing at the offending name (not a generic "not implemented").
func TestDiscoverOnce_UnknownSource_Errors(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"discover", "--once", "--source", "telepathy"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("unknown source exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "telepathy") {
		t.Errorf("stderr should name the unknown source: %q", stderr.String())
	}
}

// TestDiscoverOnce_RequiresSourceFlag forces the caller to be explicit about
// which source they want — there is no reasonable default while only one
// built-in exists, and silently picking "markdown" would mask a bug as soon
// as PR-80 lands a Gmail source. Required flags also keep the help output
// honest about what is mandatory.
func TestDiscoverOnce_RequiresSourceFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"discover", "--once"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("missing --source exit=0; want non-zero")
	}
}

// TestDiscoverOnce_RequiresOnceFlag pins the brief's note that the Phase 1
// CLI is only the `--once` form. Without --once, the command must refuse
// rather than blocking on a not-yet-implemented daemon loop. This guards
// future PR-71 from accidentally shipping the loop without owning the
// flag handling.
func TestDiscoverOnce_RequiresOnceFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"discover", "--source", "markdown"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("missing --once exit=0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "--once") {
		t.Errorf("stderr should hint about --once: %q", stderr.String())
	}
}

// TestDiscoverOnce_MarkdownRequiresFile makes it explicit that the markdown
// source needs at least one --file argument. Otherwise we would happily emit
// `[]` and the user would be left wondering why their tasks vanished.
func TestDiscoverOnce_MarkdownRequiresFile(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"discover", "--once", "--source", "markdown"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("missing --file exit=0; want non-zero")
	}
}
