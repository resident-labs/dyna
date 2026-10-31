package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentMCPExposure(t *testing.T) {
	for _, tc := range []struct {
		declared bool
		exposure string
	}{{false, "deferred"}, {true, "direct"}} {
		path := filepath.Join(t.TempDir(), "mcp.json")
		if err := WriteAgentMCPConfig(path, "areas", tc.declared); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Servers map[string]struct {
				Exposure     string            `json:"exposure"`
				Timeout      int               `json:"timeout"`
				ToolExposure map[string]string `json:"toolExposure"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			t.Fatal(err)
		}
		dyna := config.Servers["dyna"]
		if dyna.Exposure != tc.exposure || dyna.Timeout != 600 || config.Servers["shell"].Exposure != "direct" {
			t.Fatalf("%+v", config)
		}
		if !tc.declared && (dyna.ToolExposure["post"] != "direct" || dyna.ToolExposure["session"] != "direct") {
			t.Fatal(dyna)
		}
	}
}
