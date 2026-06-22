package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/config"
)

func notionCfg(dbID string) config.Config {
	c := config.Config{}
	c.Discovery.SourcesEnabled = []string{"notion"}
	c.Discovery.Notion.DatabaseID = dbID
	return c
}

func TestProbeNotion_TokenAndDatabaseOK(t *testing.T) {
	t.Setenv("MARUNAGE_NOTION_TOKEN", "ntn_x")
	out := probeNotion(context.Background(), Inputs{Cfg: notionCfg("db123")})
	if !out.OK {
		t.Fatalf("OK = false; want true. detail=%q", out.Detail)
	}
}

func TestProbeNotion_MissingTokenFailsWithHint(t *testing.T) {
	t.Setenv("MARUNAGE_NOTION_TOKEN", "")
	out := probeNotion(context.Background(), Inputs{Cfg: notionCfg("db123")})
	if out.OK {
		t.Fatalf("OK = true; want false when token missing")
	}
	if !strings.Contains(out.Detail, "MARUNAGE_NOTION_TOKEN") || out.Hint == "" {
		t.Errorf("detail=%q hint=%q; want token mentioned + a hint", out.Detail, out.Hint)
	}
}

func TestProbeNotion_MissingDatabaseFails(t *testing.T) {
	t.Setenv("MARUNAGE_NOTION_TOKEN", "ntn_x")
	out := probeNotion(context.Background(), Inputs{Cfg: notionCfg("")})
	if out.OK {
		t.Fatalf("OK = true; want false when database_id missing")
	}
	if !strings.Contains(out.Detail, "database_id") {
		t.Errorf("detail = %q; want it to mention database_id", out.Detail)
	}
}

func TestNotionSourceEnabled(t *testing.T) {
	if !notionSourceEnabled(notionCfg("x")) {
		t.Error("notionSourceEnabled = false; want true when notion is in sources_enabled")
	}
	if notionSourceEnabled(config.Config{}) {
		t.Error("notionSourceEnabled = true; want false when notion not enabled")
	}
}
