package pi

import (
	"encoding/json"
	"maps"
	"net/url"
	"os"
)

// DynaMCPURL is where the box agent serves the dyna MCP server.
const DynaMCPURL = "http://127.0.0.1:7781/dyna/"

// MCPServer is one entry of Pi's MCP config, by URL or by command.
type MCPServer struct {
	URL         string   `json:"url,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Exposure    string   `json:"exposure"`
	Description string   `json:"description,omitempty"`
	Timeout     int      `json:"timeout,omitempty"`
}

// WriteAgentMCPConfig writes the box servers and their tool exposure, with the added servers beside them. A nonempty dir is
// where shell commands start, for a crew worker whose tree is not the box's.
func WriteAgentMCPConfig(path, description, dir string, added map[string]MCPServer) error {
	shell := "http://127.0.0.1:7781/shell/"
	if dir != "" {
		shell += "?dir=" + url.QueryEscape(dir)
	}
	servers := map[string]MCPServer{
		"shell": {URL: shell, Exposure: "direct"},
		"dyna":  {URL: DynaMCPURL, Exposure: "direct", Description: description, Timeout: 600},
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
