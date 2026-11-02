package pi

import (
	"encoding/json"
	"maps"
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

// WriteAgentMCPConfig writes the dyna server and its tool exposure, with the added servers beside it.
func WriteAgentMCPConfig(path, description string, added map[string]MCPServer) error {
	servers := map[string]MCPServer{
		"dyna": {URL: DynaMCPURL, Exposure: "direct", Description: description, Timeout: 600},
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
