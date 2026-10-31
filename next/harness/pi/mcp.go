package pi

import (
	"encoding/json"
	"os"
)

// WriteAgentMCPConfig writes the box servers and their tool exposure.
func WriteAgentMCPConfig(path, description string, declared bool) error {
	type server struct {
		URL          string            `json:"url"`
		Exposure     string            `json:"exposure"`
		ToolExposure map[string]string `json:"toolExposure,omitempty"`
		Description  string            `json:"description,omitempty"`
		Timeout      int               `json:"timeout,omitempty"`
	}
	exposure := "deferred"
	var overrides map[string]string
	if declared {
		exposure = "direct"
	} else {
		overrides = map[string]string{"post": "direct", "session": "direct"}
	}
	data, err := json.Marshal(struct {
		Servers map[string]server `json:"mcpServers"`
	}{map[string]server{
		"shell": {URL: "http://127.0.0.1:7781/shell/", Exposure: "direct"},
		"dyna":  {URL: "http://127.0.0.1:7781/dyna/", Exposure: exposure, ToolExposure: overrides, Description: description, Timeout: 600},
	}})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
