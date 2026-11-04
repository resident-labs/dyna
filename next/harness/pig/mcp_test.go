package pig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentMCPExposure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := WriteAgentMCPConfig(path, "areas", map[string]MCPServer{"docs": {URL: "https://example.test/mcp", Exposure: "deferred"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Exposure     string            `json:"exposure"`
			ToolExposure map[string]string `json:"toolExposure"`
			Timeout      int               `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	dyna := config.Servers["dyna"]
	if dyna.Exposure != "deferred" || dyna.ToolExposure["post"] != "direct" || dyna.Timeout != 600 || config.Servers["docs"].Exposure != "deferred" {
		t.Fatalf("%+v", config)
	}
}
