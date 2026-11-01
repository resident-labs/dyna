package pi

import (
	"encoding/json"
	"os"
)

// DynaMCPURL is where the box agent serves the dyna MCP server.
const DynaMCPURL = "http://127.0.0.1:7781/dyna/"

// WriteAgentMCPConfig writes the box servers and their tool exposure.
func WriteAgentMCPConfig(path, description string) error {
	type server struct {
		URL         string `json:"url"`
		Exposure    string `json:"exposure"`
		Description string `json:"description,omitempty"`
		Timeout     int    `json:"timeout,omitempty"`
	}
	data, err := json.Marshal(struct {
		Servers map[string]server `json:"mcpServers"`
	}{map[string]server{
		"shell": {URL: "http://127.0.0.1:7781/shell/", Exposure: "direct"},
		"dyna":  {URL: DynaMCPURL, Exposure: "direct", Description: description, Timeout: 600},
	}})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
