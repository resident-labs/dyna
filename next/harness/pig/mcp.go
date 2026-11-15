package pig

import (
	"encoding/json"
	"maps"
	"os"
)

// DynaMCPURL is where the box agent serves the dyna MCP server.
const DynaMCPURL = "http://127.0.0.1:7781/dyna/"

// GitHubMCPURL is where the box agent passes GitHub's read-only MCP server through to the engine.
const GitHubMCPURL = "http://127.0.0.1:7781/github/"

// GitHubServer is the github server's entry: deferred like dyna's tools. Pi names its tools in the system prompt's
// deferred_tools section; the description ranks them in tool_search and answers describeNamespace().
func GitHubServer() MCPServer {
	return MCPServer{URL: GitHubMCPURL, Exposure: "deferred", Timeout: 300, Description: "Read-only GitHub through the engine: search code, issues, PRs, commits and repos; read files, branches, tags, releases, PRs with files, diffs and reviews, issues and comments, Actions runs and job logs."}
}

// MCPServer is one entry of Pi's MCP config, by URL or by command.
type MCPServer struct {
	URL      string   `json:"url,omitempty"`
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	Exposure string   `json:"exposure"`
	// ToolExposure overrides Exposure for single tools.
	ToolExposure map[string]string `json:"toolExposure,omitempty"`
	Description  string            `json:"description,omitempty"`
	Timeout      int               `json:"timeout,omitempty"`
}

// WriteAgentMCPConfig writes the dyna server and its tool exposure, with the added servers beside it. The dyna tools wait for
// tool_search, so their schemas stay out of what every request sends; post stays direct because most turns end with it, and
// goal stays direct for a session that holds goals (not root), so it keeps its plan current without a search first, and advisor
// stays direct for a root whose room has an advisor, so it asks before it commits to an approach. Its call can wait 15 minutes, so the
// server's timeout goes past that.
func WriteAgentMCPConfig(path, description string, goals, advisor bool, added map[string]MCPServer) error {
	direct := map[string]string{"post": "direct"}
	if goals {
		direct["goal"] = "direct"
	}
	if advisor {
		direct["advisor"] = "direct"
	}
	timeout := 600
	if advisor {
		timeout = 960
	}
	servers := map[string]MCPServer{
		"dyna": {URL: DynaMCPURL, Exposure: "deferred", ToolExposure: direct, Description: description, Timeout: timeout},
	}
	maps.Insert(servers, maps.All(added))
	data, err := json.Marshal(struct {
		Servers map[string]MCPServer `json:"mcpServers"`
	}{servers})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
