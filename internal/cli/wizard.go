package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/haruotsu/marunage/internal/config"
	"github.com/haruotsu/marunage/internal/logging"
	"golang.org/x/term"
)

// TTY interaction is funnelled through these function vars so
// wizard_test.go can drive the non-TTY and MakeRaw-failure branches
// without a real terminal. Production code never reassigns them; tests
// use setTTYHooksForTest in wizard_test.go which restores the
// originals on cleanup. (Mirrors the pattern in internal/secrets/passphrase.go.)
var (
	isTerminalFunc  = term.IsTerminal
	makeRawFunc     = term.MakeRaw
	restoreTermFunc = term.Restore
	getTermSizeFunc = term.GetSize
)

// sourceItem is one entry in the discovery-source selection list.
type sourceItem struct {
	key         string
	label       string
	description string
}

// knownSources is the ordered list shown to the user in the wizard.
// This is a subset of knownBuiltinNames (source_registry.go); intentionally
// excluded: "slack:reaction" (requires extra config), "googletasks", "notion"
// (experimental).
var knownSources = []sourceItem{
	{key: "markdown", label: "Markdown", description: "ローカルの Markdown TODO ファイルを監視"},
	{key: "slack", label: "Slack", description: "メンション・DM をタスク化"},
	{key: "github", label: "GitHub", description: "Issue / PR をタスク化"},
	{key: "gmail", label: "Gmail", description: "未読メールをタスク化"},
	{key: "calendar", label: "Google Calendar", description: "Google Calendar の予定をタスク化"},
}

// specialKey identifies non-printable key events.
type specialKey int

const (
	keyNone  specialKey = iota
	keyUp               // ↑ or k
	keyDown             // ↓ or j
	keySpace            // space bar (toggle)
	keyEnter            // Enter / Return (confirm)
	keyAbort            // Ctrl-C / q (cancel)
)

// keyEvent represents a single keystroke parsed from raw input.
type keyEvent struct {
	special specialKey
	ch      rune // set when special == keyNone
}

// parseKey reads the minimal byte(s) needed to identify one key event.
// It supports ANSI escape sequences for arrow keys.
func parseKey(r io.Reader) (keyEvent, error) {
	var head [1]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return keyEvent{}, err
	}
	switch head[0] {
	case '\r', '\n':
		return keyEvent{special: keyEnter}, nil
	case ' ':
		return keyEvent{special: keySpace}, nil
	case 3: // Ctrl-C
		return keyEvent{special: keyAbort}, nil
	case 'q':
		return keyEvent{special: keyAbort}, nil
	case 'k':
		return keyEvent{special: keyUp}, nil
	case 'j':
		return keyEvent{special: keyDown}, nil
	case 0x1b: // ESC — read [ then the final byte one at a time
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err == nil && b[0] == '[' {
			if _, err := io.ReadFull(r, b[:]); err == nil {
				switch b[0] {
				case 'A':
					return keyEvent{special: keyUp}, nil
				case 'B':
					return keyEvent{special: keyDown}, nil
				}
			}
		}
		return keyEvent{ch: 0x1b}, nil
	default:
		return keyEvent{ch: rune(head[0])}, nil
	}
}

// applyKeys processes a slice of keyEvents against an initial selection state
// and returns the final (cursor, selected) state. The loop stops on keyEnter
// or keyAbort. This is the pure, testable core of the selection state machine.
func applyKeys(n int, initial []bool, keys []keyEvent) (cursor int, selected []bool) {
	selected = make([]bool, n)
	copy(selected, initial)
	cursor = 0

	for _, k := range keys {
		switch k.special {
		case keyEnter, keyAbort:
			return cursor, selected
		case keyDown:
			if cursor < n-1 {
				cursor++
			}
		case keyUp:
			if cursor > 0 {
				cursor--
			}
		case keySpace:
			selected[cursor] = !selected[cursor]
		}
	}
	return cursor, selected
}

// displayWidth returns the number of terminal columns needed to render s.
// East Asian Wide and Fullwidth runes count as 2; C0 control characters and
// DEL count as 0. Other runes count as 1 — notably East Asian Ambiguous
// characters (e.g. ↑ U+2191, ↓ U+2193, ─ U+2500) are treated as 1 here,
// which matches non-CJK locale rendering and Western terminals but may
// undercount by 1 column per such char on CJK-locale terminals.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// runeDisplayWidth returns the column width of r, using a conservative
// approximation of the Unicode East Asian Width tables (no external dep).
// C0 controls (< 0x20) and DEL (0x7f) collapse to 0; C1 controls
// (0x80-0x9F) are treated as 1 since they are not expected in CLI output.
// East Asian Ambiguous runes fall through to the default of 1.
func runeDisplayWidth(r rune) int {
	if r < 0x20 || r == 0x7f {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK Radicals / Kangxi
		r >= 0x3041 && r <= 0x33FF,   // Hiragana, Katakana, CJK symbols
		r >= 0x3400 && r <= 0x4DBF,   // CJK Ext A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK Unified
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul Syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK Compatibility
		r >= 0xFE30 && r <= 0xFE4F,   // CJK Compat Forms
		r >= 0xFF00 && r <= 0xFF60,   // Fullwidth ASCII
		r >= 0xFFE0 && r <= 0xFFE6,   // Fullwidth signs
		r >= 0x20000 && r <= 0x2FFFD, // CJK Ext B-F
		r >= 0x30000 && r <= 0x3FFFD: // CJK Ext G
		return 2
	}
	return 1
}

// physicalRows returns how many terminal rows a logical line of the given
// display width occupies when rendered into a terminal of termWidth columns.
// Empty lines still occupy one row.
func physicalRows(lineWidth, termWidth int) int {
	if termWidth <= 0 {
		return 1
	}
	if lineWidth == 0 {
		return 1
	}
	return (lineWidth + termWidth - 1) / termWidth
}

// renderList draws the current selection state to out.
// It returns the number of physical terminal rows used (accounting for line
// wrapping at termWidth columns) so the caller can move the cursor up to
// redraw without leaving stale rows on screen. multiSelect feeds this value
// back to ESC[{n}A on the next redraw to rewind exactly past the previous
// frame.
func renderList(items []sourceItem, cursor int, selected []bool, out io.Writer, termWidth int) int {
	// In raw mode the terminal does not translate \n to \r\n, so every line
	// must end with \r\n to ensure the cursor returns to column 0 before the
	// next line is drawn. Otherwise the list renders as a staircase.
	rows := 0
	header := "ソースを選択（↑↓ 移動、Space で切り替え、Enter で確定）:"
	fmt.Fprint(out, header+"\r\n")
	rows += physicalRows(displayWidth(header), termWidth)

	for i, item := range items {
		check := " "
		if selected[i] {
			check = "x"
		}
		arrow := "  "
		if i == cursor {
			arrow = "> "
		}
		line := fmt.Sprintf("%s[%s] %-16s  %s", arrow, check, item.label, item.description)
		fmt.Fprint(out, line+"\r\n")
		rows += physicalRows(displayWidth(line), termWidth)
	}
	return rows
}

// multiSelect shows a keyboard-driven multi-select list on out, reading keys
// from in. It returns the final selection slice (parallel to items).
// termWidth is the rendering width in columns used to compute how many
// physical rows the redraw must rewind past. In non-TTY contexts the caller
// can pipe \r to accept the defaults.
func multiSelect(items []sourceItem, initial []bool, in io.Reader, out io.Writer, termWidth int) ([]bool, error) {
	selected := make([]bool, len(items))
	copy(selected, initial)
	cursor := 0

	nRows := renderList(items, cursor, selected, out, termWidth)

	for {
		k, err := parseKey(in)
		if err != nil {
			// EOF or closed pipe: treat as Enter (accept current state).
			break
		}

		switch k.special {
		case keyEnter:
			return selected, nil
		case keyAbort:
			return nil, fmt.Errorf("wizard aborted")
		case keyDown:
			if cursor < len(items)-1 {
				cursor++
			}
		case keyUp:
			if cursor > 0 {
				cursor--
			}
		case keySpace:
			selected[cursor] = !selected[cursor]
		}

		// Move cursor up to the start of the previous render and clear from
		// there to end of screen. ESC[J avoids leaving stragglers behind when
		// the new render is shorter than the previous one.
		fmt.Fprintf(out, "\033[%dA\r\033[J", nRows)
		nRows = renderList(items, cursor, selected, out, termWidth)
	}
	return selected, nil
}

// defaultTermWidth is the fallback column count used when out is not a TTY
// (e.g. a bytes.Buffer in tests, or stdout piped to a file). 80 mirrors the
// historical VT100 default so wrapped output stays usable on inherited
// pipelines.
const defaultTermWidth = 80

// detectTermWidth returns the column width of the terminal backing out, or
// defaultTermWidth when out is not a TTY (e.g. a bytes.Buffer in tests
// or a pipe in non-interactive contexts).
func detectTermWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		if w, _, err := getTermSizeFunc(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return defaultTermWidth
}

// initialSelection builds the parallel bool slice from the currently enabled
// source keys in cfg.
func initialSelection(cfg config.Config) []bool {
	enabled := make(map[string]bool, len(cfg.Discovery.SourcesEnabled))
	for _, s := range cfg.Discovery.SourcesEnabled {
		enabled[s] = true
	}
	sel := make([]bool, len(knownSources))
	for i, src := range knownSources {
		sel[i] = enabled[src.key]
	}
	return sel
}

// runConfigWizard is the entry point for the interactive configuration wizard.
// It loads the config at configPath, runs the multi-select source picker, and
// saves the result.
func runConfigWizard(configPath string, in io.Reader, out io.Writer) error {
	if f, ok := in.(*os.File); ok && isTerminalFunc(int(f.Fd())) {
		oldState, err := makeRawFunc(int(f.Fd()))
		if err != nil {
			fmt.Fprintf(out, "warning: failed to enter raw mode: %v\r\n", err)
		} else {
			defer func() { _ = restoreTermFunc(int(f.Fd()), oldState) }()
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", configPath, err)
	}

	// Raw mode may be on at this point (when isTerminalFunc(in) is true and
	// MakeRaw succeeded), so emit \r\n explicitly instead of Fprintln. \r\n
	// is also harmless on cooked terminals and in tests, so we use it
	// unconditionally rather than branching on the raw-mode state.
	fmt.Fprint(out, "\r\nmarunage config wizard\r\n")
	fmt.Fprint(out, strings.Repeat("─", 40)+"\r\n")

	termWidth := detectTermWidth(out)
	initial := initialSelection(cfg)
	selected, err := multiSelect(knownSources, initial, in, out, termWidth)
	if err != nil {
		return err
	}

	// Build new sources_enabled from selection.
	sources := []string{}
	for i, src := range knownSources {
		if selected[i] {
			sources = append(sources, src.key)
		}
	}

	// Encode as JSON array string for config.Set.
	raw, err := json.Marshal(sources)
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}
	if err := config.Set(&cfg, "discovery.sources_enabled", string(raw)); err != nil {
		return fmt.Errorf("set sources_enabled: %w", err)
	}

	auditPath := auditLogPathFor(configPath)
	auditor, err := logging.NewAuditLog(auditPath)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer func() { _ = auditor.Close() }()

	auditor.Record(config.AuditEvent{
		Action: "config.wizard",
		Path:   configPath,
		Key:    "discovery.sources_enabled",
		Value:  string(raw),
	})
	if err := config.Save(configPath, cfg, auditor); err != nil {
		return fmt.Errorf("save %s: %w", configPath, err)
	}

	fmt.Fprintf(out, "\r\n設定を保存しました: %s\r\n", configPath)
	return nil
}
