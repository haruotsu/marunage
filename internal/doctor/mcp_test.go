package doctor

// Test list (t_wada style):
//
//   1. parseMCPServerName: 新フォーマット行から名前のみ抽出する
//   2. parseMCPServerName: 旧フォーマット（名前のみ）はそのまま返す
//   3. parseMCPServerName: 新旧混在の各行を正しく処理する
//   4. parseMCPServerName: ": " で始まる行（名前なし）はそのまま返す（空文字列を作らない）
//   5. parseMCPServerName: コロンのみ（スペースなし）は区切り文字として扱わない

import "testing"

func TestParseMCPServerName_NewFormat(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "slack mcp",
			line: "claude.ai Slack: https://mcp.slack.com/mcp - ✓ Connected",
			want: "claude.ai Slack",
		},
		{
			name: "playwright",
			line: "playwright: npx @playwright/mcp@latest - ✓ Connected",
			want: "playwright",
		},
		{
			name: "muumuu-domain",
			line: "claude.ai muumuu-domain: https://mcp.muumuu-domain.com/mcp - ✓ Connected",
			want: "claude.ai muumuu-domain",
		},
		{
			name: "gmail needs auth",
			line: "claude.ai Gmail: https://gmailmcp.googleapis.com/mcp/v1 - ! Needs authentication",
			want: "claude.ai Gmail",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMCPServerName(tc.line); got != tc.want {
				t.Errorf("parseMCPServerName(%q) = %q; want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestParseMCPServerName_OldFormat(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"plain slack", "slack", "slack"},
		{"plain google-drive", "google-drive", "google-drive"},
		{"name with no colon space", "my-server", "my-server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMCPServerName(tc.line); got != tc.want {
				t.Errorf("parseMCPServerName(%q) = %q; want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestParseMCPServerName_MixedFormat(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"new format slack", "claude.ai Slack: https://mcp.slack.com/mcp - ✓ Connected", "claude.ai Slack"},
		{"old format slack", "slack", "slack"},
		{"new format playwright", "playwright: npx @playwright/mcp@latest - ✓ Connected", "playwright"},
		{"old format google-drive", "google-drive", "google-drive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMCPServerName(tc.line); got != tc.want {
				t.Errorf("parseMCPServerName(%q) = %q; want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestParseMCPServerName_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "leading colon-space returns line unchanged",
			line: ": some-url - status",
			want: ": some-url - status",
		},
		{
			name: "colon without space is not a separator",
			line: "my:server",
			want: "my:server",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseMCPServerName(tc.line); got != tc.want {
				t.Errorf("parseMCPServerName(%q) = %q; want %q", tc.line, got, tc.want)
			}
		})
	}
}
