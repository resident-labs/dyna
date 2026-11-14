package pig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The github server waits for tool_search like dyna's tools, and its one-line description fits the section Pi gives it.
func TestGitHubServerIsDeferred(t *testing.T) {
	server := GitHubServer()
	if len(server.Description) > 250 || server.Exposure != "deferred" || server.URL != GitHubMCPURL {
		t.Fatalf("%+v (%d characters)", server, len(server.Description))
	}
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := WriteAgentMCPConfig(path, "areas", false, false, map[string]MCPServer{"github": server}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]MCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Servers["github"]; got.URL != "http://127.0.0.1:7781/github/" || got.Exposure != "deferred" || config.Servers["dyna"].URL != DynaMCPURL {
		t.Fatalf("%+v", config.Servers)
	}
}
