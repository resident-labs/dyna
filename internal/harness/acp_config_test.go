package harness

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
)

func TestKimiACPMCPUsesCatalog(t *testing.T) {
	generation := t.TempDir()
	doc := `{"mcpServers":{"dyna":{"command":"/private/dyna","args":["mcp","shell"],"env":{}},"other":{"command":"/other","args":[]}}}`
	if err := os.WriteFile(filepath.Join(generation, "settings.json"), []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessKimiCode, ExtraArgs: []string{"--add-dir", generation}}
	servers, err := acpMCPServers(p, []string{"DYNA_SESSION=kimi-session", "DYNA_SCRATCH=/scratch/kimi"})
	if err != nil || len(servers) != 1 {
		t.Fatalf("Kimi catalog servers: %+v, %v", servers, err)
	}
	server := servers[0]
	if server.Name != "dyna" || server.Command != "/private/dyna" || len(server.Args) != 2 || server.Args[0] != "mcp" || server.Args[1] != "shell" {
		t.Fatalf("Kimi catalog server: %+v", server)
	}
	env := map[string]string{}
	for _, variable := range server.Env {
		env[variable.Name] = variable.Value
	}
	if env["DYNA_SESSION"] != "kimi-session" || env["DYNA_SCRATCH"] != "/scratch/kimi" {
		t.Fatalf("Kimi MCP environment: %v", env)
	}
}
