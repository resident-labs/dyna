package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentMCPExposure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := WriteAgentMCPConfig(path, "areas"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Exposure string `json:"exposure"`
			Timeout  int    `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Servers["dyna"].Exposure != "direct" || config.Servers["dyna"].Timeout != 600 || config.Servers["shell"].Exposure != "direct" {
		t.Fatalf("%+v", config)
	}
}
