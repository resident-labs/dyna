package pi

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
// tool_search, so their schemas stay out of what every request sends, and post stays direct because most turns end with it.
func WriteAgentMCPConfig(path, description string, added map[string]MCPServer) error {
	servers := map[string]MCPServer{
		"dyna": {URL: DynaMCPURL, Exposure: "deferred", ToolExposure: map[string]string{"post": "direct"}, Description: description, Timeout: 600},
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
