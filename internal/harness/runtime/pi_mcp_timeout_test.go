package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/shellmcp"
)

// Pi reads a per-server request timeout and exposure from mcp.json. Dyna's
// shell server waits up to ten minutes for a job, so its timeout must outlast
// that wait, and a catalog entry must set both fields itself.
func TestPiMCPTimeoutAndExposure(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	slow := harnessmgr.ManagedMCP{Name: "slow", MCPDefinition: harnessmgr.MCPDefinition{
		Command: "slow", TimeoutSeconds: 300, Exposure: harnessmgr.MCPExposureDeferred,
	}, EnabledHarnesses: []string{"pi"}}
	if err := r.Config.AddMCP(owner, slow); err != nil {
		t.Fatal(err)
	}
	g := buildGeneration(t, r, "pi", home, id)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "pi-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		MCP map[string]struct {
			Timeout  int    `json:"timeout"`
			Exposure string `json:"exposure"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if entry := root.MCP["slow"]; entry.Timeout != 300 || entry.Exposure != "deferred" {
		t.Fatalf("catalog server = %+v, want timeout 300 and deferred exposure in %s", entry, raw)
	}
	wantShell := int(shellmcp.MaxRequestTimeout.Seconds())
	if entry := root.MCP[dynaMCPName]; entry.Timeout != wantShell || entry.Exposure != "direct" {
		t.Fatalf("dyna shell server = %+v, want timeout %d and direct exposure in %s", entry, wantShell, raw)
	}
}
