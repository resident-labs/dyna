package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestCatalogWritesUseCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := config.NewConfigManager()
	if err := os.MkdirAll(filepath.Dir(manager.Path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct {
		name, action string
		args         []any
	}{
		{"AddHarnessMCP", "mcp add", []any{config.ManagedMCP{Name: "new", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{}}}},
		{"AddHarnessSkill", "skill add", []any{config.ManagedSkill{Name: "new", Source: source, EnabledHarnesses: []string{}}}},
		{"RemoveHarnessConfig", "mcp rm", []any{"mcp", "existing", ""}},
		{"RemoveHarnessConfig", "skill rm", []any{"skill", "existing", ""}},
		{"RemoveHarnessConfig", "mcp rm scoped", []any{"mcp", "existing", "codex"}},
		{"RemoveHarnessConfig", "skill rm scoped", []any{"skill", "existing", "codex"}},
		{"SetHarnessConfigEnabled", "mcp enable", []any{"mcp", "existing", "codex", true}},
		{"SetHarnessConfigEnabled", "skill enable", []any{"skill", "existing", "codex", true}},
		{"SetHarnessConfigEnabled", "mcp disable", []any{"mcp", "existing", "codex", false}},
		{"SetHarnessConfigEnabled", "skill disable", []any{"skill", "existing", "codex", false}},
	} {
		t.Run(op.action, func(t *testing.T) {
			for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, authority.Steward, ""} {
				t.Run(string(role), func(t *testing.T) {
					enabled := []string{"codex"}
					if strings.HasSuffix(op.action, " enable") {
						enabled = []string{}
					}
					baseline, err := json.Marshal(config.HarnessConfig{
						MCP:    []config.ManagedMCP{{Name: "existing", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: enabled}},
						Skills: []config.ManagedSkill{{Name: "existing", Source: source, EnabledHarnesses: enabled}},
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(manager.Path, baseline, 0600); err != nil {
						t.Fatal(err)
					}
					actor := authority.Actor{Role: role, Session: "session", Run: "wf_test", Room: "room"}
					caller := websvc.Caller{Kind: websvc.Session, Session: actor.Session}
					if role == authority.Owner {
						caller = websvc.Caller{Kind: websvc.Owner}
					}
					if role == authority.Worker {
						caller = websvc.Caller{Kind: websvc.Worker, Run: actor.Run}
					}
					if role == "" {
						caller = websvc.Caller{Kind: websvc.Unverified}
						actor = authority.Actor{}
					}
					_, err = calls[op.name](localCallerContext(t, caller, actor), cliArgs(t, op.args...))
					allow := role == authority.Owner || role == authority.Root
					if allow && err != nil {
						t.Fatalf("allowed catalog write: %v", err)
					}
					if !allow && err == nil {
						t.Errorf("role %q changed the global catalog", role)
					}
					if !allow && role != "" && err != nil && !strings.Contains(err.Error(), strings.TrimSuffix(op.action, " scoped")+" is owner or root only") {
						t.Errorf("role %q got the wrong refusal: %v", role, err)
					}
					after, readErr := os.ReadFile(manager.Path)
					if readErr != nil {
						t.Fatal(readErr)
					}
					if !allow && !bytes.Equal(baseline, after) {
						t.Error("refused operation changed the catalog")
					}
					if allow && bytes.Equal(baseline, after) {
						t.Error("allowed operation did not change the catalog")
					}
				})
			}
		})
	}
}
