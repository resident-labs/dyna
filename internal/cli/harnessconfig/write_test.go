package harnessconfig

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
	"github.com/spf13/cobra"
)

func executeWrite(kind string, args ...string) (string, error) {
	c := &cobra.Command{Use: kind}
	AddWriteCommands(c, kind)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.ExecuteContext(authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner}))
	return out.String(), err
}
func TestWriteCommandsCatalog(t *testing.T) {
	isolateCatalog(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	serveCatalogEngine(t, authority.Actor{Role: authority.Owner})
	personal := filepath.Join(home, ".claude.json")
	if e := os.WriteFile(personal, []byte("personal-canary"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{
		{"add", "tool", "--command", "node", "--arg", "first", "--arg", "second", "--env-key", "CLI_TEST_SECRET", "--harness", "claude"},
		{"disable", "tool", "--harness", "claude"},
		{"enable", "tool", "--harness", "pi"},
	} {
		if _, e := executeWrite("mcp", args...); e != nil {
			t.Fatal(e)
		}
	}
	c, e := harnessmgr.NewConfigManager().Read()
	if e != nil {
		t.Fatal(e)
	}
	if len(c.MCP) != 1 || strings.Join(c.MCP[0].Args, ",") != "first,second" || strings.Join(c.MCP[0].EnabledHarnesses, ",") != "pi" {
		t.Fatalf("unexpected catalog: %+v", c)
	}
	if b, e := os.ReadFile(personal); e != nil || string(b) != "personal-canary" {
		t.Fatal("personal file changed")
	}
	if _, e := executeWrite("mcp", "rm", "tool"); e != nil {
		t.Fatal(e)
	}
}
func TestSkillWriteCommands(t *testing.T) {
	isolateCatalog(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	serveCatalogEngine(t, authority.Actor{Role: authority.Owner})
	source := t.TempDir()
	if e := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("skill"), 0600); e != nil {
		t.Fatal(e)
	}
	read := func() harnessmgr.HarnessConfig {
		t.Helper()
		c, err := harnessmgr.NewConfigManager().Read()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, e := executeWrite("skill", "add", "review", "--source", source); e != nil {
		t.Fatal(e)
	}
	if c := read(); len(c.Skills) != 1 || !c.Skills[0].Enabled {
		t.Fatalf("add did not default to enabled: %+v", c.Skills)
	}
	if _, e := executeWrite("skill", "disable", "review"); e != nil {
		t.Fatal(e)
	}
	if c := read(); len(c.Skills) != 1 || c.Skills[0].Enabled {
		t.Fatalf("disable was not saved: %+v", c.Skills)
	}
	if _, e := executeWrite("skill", "enable", "review"); e != nil {
		t.Fatal(e)
	}
	if c := read(); len(c.Skills) != 1 || !c.Skills[0].Enabled {
		t.Fatalf("enable was not saved: %+v", c.Skills)
	}
	if _, e := executeWrite("skill", "rm", "review"); e != nil {
		t.Fatal(e)
	}
	if c := read(); len(c.Skills) != 0 {
		t.Fatalf("rm was not saved: %+v", c.Skills)
	}
	// The skill catalog is global: no harness flag and no sync verb.
	if _, e := executeWrite("skill", "add", "other", "--source", source, "--harness", "claude"); e == nil || !strings.Contains(e.Error(), "unknown flag: --harness") {
		t.Fatalf("skill add accepted --harness: %v", e)
	}
	if _, e := executeWrite("skill", "sync", "--harness", "pi", "--account", "selected"); e == nil || !strings.Contains(e.Error(), "unknown command") {
		t.Fatalf("skill sync exists: %v", e)
	}
}
