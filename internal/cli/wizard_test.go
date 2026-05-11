package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/term"
)

// setTTYHooksForTest swaps the TTY-related function vars used by
// runConfigWizard so tests can drive the non-TTY and MakeRaw-failure
// branches without a real terminal. The returned func restores the
// originals and should be deferred.
func setTTYHooksForTest(
	isTerminal func(fd int) bool,
	makeRaw func(fd int) (*term.State, error),
	restoreTerm func(fd int, oldState *term.State) error,
) func() {
	prevIsTerm := isTerminalFunc
	prevMakeRaw := makeRawFunc
	prevRestore := restoreTermFunc
	isTerminalFunc = isTerminal
	makeRawFunc = makeRaw
	restoreTermFunc = restoreTerm
	return func() {
		isTerminalFunc = prevIsTerm
		makeRawFunc = prevMakeRaw
		restoreTermFunc = prevRestore
	}
}

// テストリスト:
// 1. applyKeys: Enter だけで初期選択がそのまま返る
// 2. applyKeys: Space でカーソル位置の選択が反転する
// 3. applyKeys: Down でカーソルが次に移動する（末尾でクランプ）
// 4. applyKeys: Up でカーソルが前に移動する（先頭でクランプ）
// 5. applyKeys: Down→Space→Enter で 2 番目が追加選択される
// 6. applyKeys: Down を末尾でクランプする
// 7. multiSelect: Enter を受け取ると初期選択を返す
// 8. multiSelect: Space→Enter で選択が反転される
// 9. multiSelect: 出力にソースラベルが含まれる
// 10. runConfigWizard: 現在有効なソースが事前選択される
// 11. runConfigWizard: ウィザード完了後に sources_enabled が保存される
// 12. runConfigWizard: 全未選択で空スライスが保存される
// 13. config wizard サブコマンドが存在する
// 14. knownSources のキーはすべて knownBuiltinNames に含まれる
// 15. parseKey: ESC[A / ESC[B が 1 バイトずつ届いても矢印として解釈される
// 16. parseKey: ESC 単独（後続なし）は keyEvent{ch:0x1b} にフォールバック
// 17. parseKey: ESC[ の後に未知バイトが来ても keyEvent{ch:0x1b} にフォールバック
// 18. runConfigWizard: 非 TTY の *os.File 入力では raw mode に入らない
// 19. runConfigWizard: MakeRaw 失敗時は warning を out に出して処理を継続する
// 20. renderList: 各行は \r\n 終端（raw mode で行頭に戻るため）
// 21. displayWidth: ASCII 文字は 1 カラム
// 22. displayWidth: 全角(東アジア幅)文字は 2 カラム
// 23. physicalRows: displayWidth <= termWidth なら 1 行
// 24. physicalRows: termWidth+1 なら 2 行（折り返し）
// 25. physicalRows: displayWidth=0 でも最低 1 行
// 26. renderList: termWidth が狭いときは折り返しを加味した物理行数を返す
// 27. multiSelect: リドロー時の ESC[%dA に renderList が返した物理行数 (折り返し加味) が渡る、かつ ESC[J (clear to EOS) が後続する

// --- applyKeys unit tests ---

func TestApplyKeys_EnterReturnsInitial(t *testing.T) {
	initial := []bool{true, false, false}
	cursor, got := applyKeys(3, initial, []keyEvent{{special: keyEnter}})
	if cursor != 0 {
		t.Errorf("cursor=%d; want 0", cursor)
	}
	want := []bool{true, false, false}
	for i, v := range got {
		if v != want[i] {
			t.Errorf("selected[%d]=%v; want %v", i, v, want[i])
		}
	}
}

func TestApplyKeys_SpaceTogglesCurrentItem(t *testing.T) {
	initial := []bool{false, false, false}
	_, got := applyKeys(3, initial, []keyEvent{
		{special: keySpace},
		{special: keyEnter},
	})
	if !got[0] {
		t.Errorf("selected[0]=%v; want true after Space", got[0])
	}
	if got[1] || got[2] {
		t.Errorf("selected[1]=%v selected[2]=%v; want false", got[1], got[2])
	}
}

func TestApplyKeys_DownMovesCursor(t *testing.T) {
	initial := []bool{false, false, false}
	cursor, _ := applyKeys(3, initial, []keyEvent{
		{special: keyDown},
		{special: keyEnter},
	})
	if cursor != 1 {
		t.Errorf("cursor=%d; want 1 after Down", cursor)
	}
}

func TestApplyKeys_UpClampAtZero(t *testing.T) {
	initial := []bool{false, false}
	cursor, _ := applyKeys(2, initial, []keyEvent{
		{special: keyUp},
		{special: keyEnter},
	})
	if cursor != 0 {
		t.Errorf("cursor=%d; want 0 (clamped)", cursor)
	}
}

func TestApplyKeys_DownSpaceEnterSelectsSecond(t *testing.T) {
	initial := []bool{false, false, false}
	_, got := applyKeys(3, initial, []keyEvent{
		{special: keyDown},
		{special: keySpace},
		{special: keyEnter},
	})
	if got[1] == false {
		t.Errorf("selected[1]=%v; want true", got[1])
	}
	if got[0] || got[2] {
		t.Errorf("selected[0]=%v selected[2]=%v; want false", got[0], got[2])
	}
}

func TestApplyKeys_DownClampAtEnd(t *testing.T) {
	initial := []bool{false, false}
	cursor, _ := applyKeys(2, initial, []keyEvent{
		{special: keyDown},
		{special: keyDown},
		{special: keyDown},
		{special: keyEnter},
	})
	if cursor != 1 {
		t.Errorf("cursor=%d; want 1 (clamped at end)", cursor)
	}
}

// --- multiSelect integration tests ---

func TestMultiSelect_EnterImmediatelyReturnsInitial(t *testing.T) {
	items := []sourceItem{
		{key: "a", label: "A", description: "desc a"},
		{key: "b", label: "B", description: "desc b"},
	}
	initial := []bool{true, false}

	// just Enter
	in := bytes.NewBufferString("\r")
	var out bytes.Buffer
	got, err := multiSelect(items, initial, in, &out, 80)
	if err != nil {
		t.Fatalf("multiSelect err=%v", err)
	}
	if !got[0] || got[1] {
		t.Errorf("got=%v; want [true false]", got)
	}
}

func TestMultiSelect_SpaceEnterTogglesFirst(t *testing.T) {
	items := []sourceItem{
		{key: "a", label: "A", description: "desc a"},
	}
	initial := []bool{false}

	in := bytes.NewBufferString(" \r") // space then enter
	var out bytes.Buffer
	got, err := multiSelect(items, initial, in, &out, 80)
	if err != nil {
		t.Fatalf("multiSelect err=%v", err)
	}
	if !got[0] {
		t.Errorf("got[0]=%v; want true after Space", got[0])
	}
}

func TestMultiSelect_OutputContainsSourceLabels(t *testing.T) {
	items := []sourceItem{
		{key: "slack", label: "Slack", description: "Slack desc"},
		{key: "github", label: "GitHub", description: "GitHub desc"},
	}
	in := bytes.NewBufferString("\r")
	var out bytes.Buffer
	if _, err := multiSelect(items, []bool{false, false}, in, &out, 80); err != nil {
		t.Fatalf("multiSelect err=%v", err)
	}

	rendered := out.String()
	if !strings.Contains(rendered, "Slack") {
		t.Errorf("output does not contain 'Slack'; got: %q", rendered)
	}
	if !strings.Contains(rendered, "GitHub") {
		t.Errorf("output does not contain 'GitHub'; got: %q", rendered)
	}
}

// --- runConfigWizard integration tests ---

func TestRunConfigWizard_PreSelectsEnabledSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	// まず slack を有効にした設定を書き込む
	code := Execute([]string{"--config", path, "config", "set",
		"discovery.sources_enabled", `["slack"]`}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatal("seed config set failed")
	}

	// Enter だけで受け入れる（初期選択を保持するはず）
	in := bytes.NewBufferString("\r")
	var out bytes.Buffer
	if err := runConfigWizard(path, in, &out); err != nil {
		t.Fatalf("runConfigWizard: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code = Execute([]string{"--config", path, "config", "get",
		"discovery.sources_enabled"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "slack") {
		t.Errorf("sources_enabled=%q; want to contain 'slack'", got)
	}
}

func TestRunConfigWizard_SavesSourcesEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	// 全ソース未選択の初期状態 → Down + Space (slack 選択) + Enter
	in := bytes.NewBufferString("\x1b[B \r") // Down, Space, Enter
	var out bytes.Buffer
	if err := runConfigWizard(path, in, &out); err != nil {
		t.Fatalf("runConfigWizard: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"--config", path, "config", "get",
		"discovery.sources_enabled"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	// 2番目のソース(knownSources[1])が選択されているはず
	want := knownSources[1].key
	if !strings.Contains(got, want) {
		t.Errorf("sources_enabled=%q; want to contain %q", got, want)
	}
}

func TestRunConfigWizard_EmptySelectionSavesEmptySlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	// まず sources_enabled を空にシードする
	code := Execute([]string{"--config", path, "config", "set",
		"discovery.sources_enabled", "[]"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatal("seed config set failed")
	}

	// 全未選択状態で Enter だけ → sources_enabled = []
	in := bytes.NewBufferString("\r")
	var out bytes.Buffer
	if err := runConfigWizard(path, in, &out); err != nil {
		t.Fatalf("runConfigWizard: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code = Execute([]string{"--config", path, "config", "get",
		"discovery.sources_enabled"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("config get exit=%d stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "[]" && got != "" {
		t.Errorf("sources_enabled=%q; want [] or empty for no selection", got)
	}
}

// --- CLI subcommand test ---

func TestConfigWizard_NonTTYCompletesWithEnter(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"config", "wizard"})
	if err != nil || cmd.Use != "wizard" {
		t.Fatalf("config wizard command not found (Use=%q, err=%v)", cmd.Use, err)
	}
}

// oneByteReader wraps a []byte and returns exactly 1 byte per Read call.
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

func TestParseKey_EscUpOneByte(t *testing.T) {
	// ESC [ A — delivered one byte at a time
	r := &oneByteReader{data: []byte{0x1b, '[', 'A'}}
	k, err := parseKey(r)
	if err != nil {
		t.Fatalf("parseKey err=%v", err)
	}
	if k.special != keyUp {
		t.Errorf("got special=%v; want keyUp", k.special)
	}
}

func TestParseKey_EscDownOneByte(t *testing.T) {
	// ESC [ B — delivered one byte at a time
	r := &oneByteReader{data: []byte{0x1b, '[', 'B'}}
	k, err := parseKey(r)
	if err != nil {
		t.Fatalf("parseKey err=%v", err)
	}
	if k.special != keyDown {
		t.Errorf("got special=%v; want keyDown", k.special)
	}
}

func TestParseKey_EscAloneFallback(t *testing.T) {
	// ESC のみ（後続バイトなし、EOF）
	r := &oneByteReader{data: []byte{0x1b}}
	k, err := parseKey(r)
	if err != nil {
		t.Fatalf("parseKey err=%v", err)
	}
	if k.special != keyNone {
		t.Errorf("got special=%v; want keyNone", k.special)
	}
	if k.ch != 0x1b {
		t.Errorf("got ch=%#x; want 0x1b", k.ch)
	}
}

func TestParseKey_EscBracketUnknownFallback(t *testing.T) {
	// ESC [ の後に未知のバイト 'C' が来た場合
	r := &oneByteReader{data: []byte{0x1b, '[', 'C'}}
	k, err := parseKey(r)
	if err != nil {
		t.Fatalf("parseKey err=%v", err)
	}
	if k.special != keyNone {
		t.Errorf("got special=%v; want keyNone", k.special)
	}
	if k.ch != 0x1b {
		t.Errorf("got ch=%#x; want 0x1b", k.ch)
	}
}

// TestRunConfigWizard_NonTTYFileSkipsRawMode verifies that when in is an
// *os.File that is not a TTY, runConfigWizard skips the raw-mode setup
// entirely (makeRawFunc must NOT be called) and still completes normally.
func TestRunConfigWizard_NonTTYFileSkipsRawMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = rPipe.Close() }()

	// Write Enter so multiSelect terminates, then close the writer to
	// surface EOF.
	go func() {
		_, _ = wPipe.Write([]byte("\r"))
		_ = wPipe.Close()
	}()

	makeRawCalls := 0
	restore := setTTYHooksForTest(
		func(fd int) bool { return false },
		func(fd int) (*term.State, error) {
			makeRawCalls++
			return nil, errors.New("must not be called")
		},
		func(fd int, oldState *term.State) error { return nil },
	)
	defer restore()

	var out bytes.Buffer
	if err := runConfigWizard(path, rPipe, &out); err != nil {
		t.Fatalf("runConfigWizard: %v", err)
	}
	if makeRawCalls != 0 {
		t.Errorf("makeRawFunc called %d times; want 0 on non-TTY", makeRawCalls)
	}
}

// TestRunConfigWizard_MakeRawFailureShowsWarning verifies that when
// isTerminalFunc reports true but makeRawFunc returns an error, the
// wizard continues running and prints a warning to out instead of
// silently swallowing the failure.
func TestRunConfigWizard_MakeRawFailureShowsWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")

	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = rPipe.Close() }()

	go func() {
		_, _ = wPipe.Write([]byte("\r"))
		_ = wPipe.Close()
	}()

	restore := setTTYHooksForTest(
		func(fd int) bool { return true },
		func(fd int) (*term.State, error) { return nil, errors.New("boom") },
		func(fd int, oldState *term.State) error { return nil },
	)
	defer restore()

	var out bytes.Buffer
	if err := runConfigWizard(path, rPipe, &out); err != nil {
		t.Fatalf("runConfigWizard: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "warning") {
		t.Errorf("out does not contain 'warning'; got: %q", rendered)
	}
	if !strings.Contains(rendered, "boom") {
		t.Errorf("out does not contain underlying error 'boom'; got: %q", rendered)
	}
}

// TestRenderList_LinesEndWithCRLF verifies that renderList emits \r\n at the
// end of every line. In raw mode the terminal does not translate \n to \r\n,
// so without an explicit carriage return each subsequent line starts at the
// column where the previous one ended, producing a staircase layout.
func TestRenderList_LinesEndWithCRLF(t *testing.T) {
	items := []sourceItem{
		{key: "a", label: "A", description: "desc a"},
		{key: "b", label: "B", description: "desc b"},
	}
	var out bytes.Buffer
	n := renderList(items, 0, []bool{false, false}, &out, 200)
	if n != 3 {
		t.Fatalf("renderList returned %d lines; want 3 (header + 2 items)", n)
	}
	got := out.String()
	if strings.Count(got, "\r\n") != 3 {
		t.Errorf("expected 3 \\r\\n line terminators; got %d in %q", strings.Count(got, "\r\n"), got)
	}
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("found bare \\n (not preceded by \\r) in output: %q", got)
	}
}

func TestDisplayWidth_ASCII(t *testing.T) {
	if got := displayWidth("Markdown"); got != 8 {
		t.Errorf("displayWidth(\"Markdown\")=%d; want 8", got)
	}
}

func TestDisplayWidth_FullWidth(t *testing.T) {
	// 5 fullwidth Japanese chars = 10 columns
	if got := displayWidth("ローカルの"); got != 10 {
		t.Errorf("displayWidth(\"ローカルの\")=%d; want 10", got)
	}
}

func TestDisplayWidth_Mixed(t *testing.T) {
	// "[x] Markdown" + 2 cols Japanese = "[x] Markdown" (12) + "あ" (2) = 14
	if got := displayWidth("[x] Markdownあ"); got != 14 {
		t.Errorf("displayWidth(...)=%d; want 14", got)
	}
}

func TestPhysicalRows_FitsInOneRow(t *testing.T) {
	if got := physicalRows(40, 80); got != 1 {
		t.Errorf("physicalRows(40,80)=%d; want 1", got)
	}
	if got := physicalRows(80, 80); got != 1 {
		t.Errorf("physicalRows(80,80)=%d; want 1", got)
	}
}

func TestPhysicalRows_Wraps(t *testing.T) {
	if got := physicalRows(81, 80); got != 2 {
		t.Errorf("physicalRows(81,80)=%d; want 2", got)
	}
	if got := physicalRows(161, 80); got != 3 {
		t.Errorf("physicalRows(161,80)=%d; want 3", got)
	}
}

func TestPhysicalRows_EmptyIsAtLeastOne(t *testing.T) {
	if got := physicalRows(0, 80); got != 1 {
		t.Errorf("physicalRows(0,80)=%d; want 1", got)
	}
}

func TestRenderList_PhysicalRowCountWithNarrowTerm(t *testing.T) {
	items := []sourceItem{
		{key: "a", label: "A", description: "短い"},
		{key: "b", label: "B", description: "短い"},
	}
	// Wide terminal: header + 2 items = 3 rows.
	var wide bytes.Buffer
	if n := renderList(items, 0, []bool{false, false}, &wide, 200); n != 3 {
		t.Errorf("renderList(termWidth=200)=%d; want 3", n)
	}
	// Narrow terminal: each line wraps, so physical rows > 3.
	var narrow bytes.Buffer
	if n := renderList(items, 0, []bool{false, false}, &narrow, 10); n <= 3 {
		t.Errorf("renderList(termWidth=10)=%d; want >3 (lines should wrap)", n)
	}
}

// TestMultiSelect_RedrawUsesPhysicalRowCount は本 PR 核心の回帰を直接保護する。
// 狭い端末で行が折り返したとき、リドローの巻き戻し量 (ESC[%dA) が論理行数の
// ままだと前回描画の末尾が消し残されてゴミが残る。ここでは termWidth=10 で
// 折り返しを強制し、巻き戻しシーケンスに「初回 renderList が返した物理行数」
// がそのまま現れること、続けて clear-to-EOS (ESC[J) が出ることを assert する。
func TestMultiSelect_RedrawUsesPhysicalRowCount(t *testing.T) {
	items := []sourceItem{
		{key: "a", label: "AAAAAAAA", description: "longlonglonglong"},
		{key: "b", label: "BBBBBBBB", description: "longlonglonglong"},
	}

	// 期待値: 同じ items / termWidth で renderList を呼んだときの物理行数。
	var ref bytes.Buffer
	wantRows := renderList(items, 0, []bool{false, false}, &ref, 10)
	if wantRows <= 3 {
		t.Fatalf("setup: expected wrap-induced rows>3, got %d", wantRows)
	}

	in := bytes.NewBufferString("\x1b[B\r") // Down, Enter
	var out bytes.Buffer
	if _, err := multiSelect(items, []bool{false, false}, in, &out, 10); err != nil {
		t.Fatalf("multiSelect err=%v", err)
	}

	rendered := out.String()
	wantRewind := fmt.Sprintf("\x1b[%dA", wantRows)
	if !strings.Contains(rendered, wantRewind) {
		t.Errorf("output missing redraw rewind %q; got %q", wantRewind, rendered)
	}
	if !strings.Contains(rendered, "\x1b[J") {
		t.Errorf("output missing clear-to-EOS \"\\x1b[J\" in %q", rendered)
	}
	// 巻き戻し直後に clear-to-EOS が来ること (順序保証)。
	if idxR := strings.Index(rendered, wantRewind); idxR >= 0 {
		if idxJ := strings.Index(rendered[idxR:], "\x1b[J"); idxJ < 0 {
			t.Errorf("ESC[J does not follow ESC[%%dA in %q", rendered)
		}
	}
}

// TestKnownSourcesKeysMatchBuiltinRegistry ensures every key in knownSources
// is a valid builtin plugin name so that wizard selections can actually be activated.
func TestKnownSourcesKeysMatchBuiltinRegistry(t *testing.T) {
	valid := make(map[string]bool, len(knownBuiltinNames))
	for _, name := range knownBuiltinNames {
		valid[name] = true
	}
	for _, src := range knownSources {
		if !valid[src.key] {
			t.Errorf("knownSources key %q is not in knownBuiltinNames", src.key)
		}
	}
}
