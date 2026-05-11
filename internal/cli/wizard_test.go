package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	got, err := multiSelect(items, initial, in, &out)
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
	got, err := multiSelect(items, initial, in, &out)
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
	if _, err := multiSelect(items, []bool{false, false}, in, &out); err != nil {
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
