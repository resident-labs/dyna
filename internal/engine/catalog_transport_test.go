package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestCatalogCallsKeepClaimedSessionPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	ctx := callspec.WithClaim(
		localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner}),
		callspec.Claim{Session: "claimed-session"},
	)
	entry := config.ManagedMCP{Name: "fixture", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{}}
	for _, tc := range []struct {
		name string
		args []json.RawMessage
	}{
		{"AddCatalogMCP", cliArgs(t, entry, "codex")},
		{"AddCatalogSkill", cliArgs(t, config.ManagedSkill{}, "")},
		{"RemoveCatalogEntry", cliArgs(t, "mcp", "fixture", "")},
		{"SetCatalogEntryEnabled", cliArgs(t, "skill", "fixture", "codex", true)},
		// Existing browser argument shapes still meet the shared manager policy.
		{"AddHarnessMCP", cliArgs(t, entry)},
		{"AddHarnessSkill", cliArgs(t, config.ManagedSkill{})},
		{"RemoveHarnessConfig", cliArgs(t, "mcp", "fixture", "")},
		{"SetHarnessConfigEnabled", cliArgs(t, "skill", "fixture", "codex", false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := calls[tc.name](ctx, tc.args)
			if err == nil || !strings.Contains(err.Error(), "is owner or root only") {
				t.Fatalf("claimed session reached catalog mutation: %v", err)
			}
		})
	}
}
