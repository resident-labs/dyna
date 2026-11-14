package pig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentMCPExposure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := WriteAgentMCPConfig(path, "areas", true, false, map[string]MCPServer{"docs": {URL: "https://example.test/mcp", Exposure: "deferred"}}); err != nil {
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
	if dyna.Exposure != "deferred" || dyna.ToolExposure["post"] != "direct" || dyna.ToolExposure["goal"] != "direct" || dyna.Timeout != 600 || config.Servers["docs"].Exposure != "deferred" {
		t.Fatalf("%+v", config)
	}
}

func TestAgentMCPExposesAdvisorToARootWithOne(t *testing.T) {
	exposure := func(advisor bool) (map[string]string, int) {
		path := filepath.Join(t.TempDir(), "mcp.json")
		if err := WriteAgentMCPConfig(path, "areas", false, advisor, nil); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Servers map[string]struct {
				ToolExposure map[string]string `json:"toolExposure"`
				Timeout      int               `json:"timeout"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		return config.Servers["dyna"].ToolExposure, config.Servers["dyna"].Timeout
	}
	if direct, timeout := exposure(false); direct["advisor"] != "" || timeout != 600 {
		t.Fatalf("without an advisor: %v %d", direct, timeout)
	}
	if direct, timeout := exposure(true); direct["advisor"] != "direct" || timeout <= 15*60 {
		t.Fatalf("with an advisor: %v %d, which must outlast the 15 minute wait", direct, timeout)
	}
}
