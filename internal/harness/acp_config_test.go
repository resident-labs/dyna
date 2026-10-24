package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

func TestAntigravityACPRequiresItsOwnPrivateLogin(t *testing.T) {
	home := t.TempDir()
	cliToken := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(cliToken, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliToken, "antigravity-oauth-token"), []byte("CLI token is not an ACP grant"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateAntigravityACPLogin(home); err == nil || !strings.Contains(err.Error(), "acp_token.json") {
		t.Fatalf("CLI login was accepted for ACP: %v", err)
	}
	dir := filepath.Join(home, "antigravity-acp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "acp_token.json")
	if err := os.WriteFile(token, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateAntigravityACPLogin(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateAntigravityACPLogin(home); err == nil {
		t.Fatal("accepted readable ACP credentials")
	}
}

func TestAntigravityACPMCPUsesCatalogAndLaunchEnvironment(t *testing.T) {
	generation := t.TempDir()
	doc := `{"mcpServers":{"dyna":{"command":"/private/dyna","args":["mcp","serve"],"env":{"FIXTURE":"catalog"}},"tools":{"command":"/tools","args":[],"env":{"ONLY":"tools"}}}}`
	if err := os.WriteFile(filepath.Join(generation, "settings.json"), []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessAntigravity, ExtraArgs: []string{"--add-dir", generation}}
	servers, err := acpMCPServers(p, []string{"DYNA_SESSION=exact-session", "DYNA_SCRATCH=/scratch/session", "FIXTURE=launch", "PRIVATE_MARKER=launch-only"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Name != "dyna" || servers[1].Name != "tools" {
		t.Fatalf("catalog servers: %+v", servers)
	}
	env := map[string]string{}
	for _, v := range servers[0].Env {
		env[v.Name] = v.Value
	}
	if env["DYNA_SESSION"] != "exact-session" || env["DYNA_SCRATCH"] != "/scratch/session" || env["FIXTURE"] != "catalog" {
		t.Fatalf("Dyna MCP environment: %v", env)
	}
	if len(servers[1].Env) != 1 || servers[1].Env[0].Name != "ONLY" {
		t.Fatalf("unrelated server inherited shell environment: %+v", servers[1])
	}
}

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

func TestAntigravityACPPreservesCatalogURLServers(t *testing.T) {
	generation := t.TempDir()
	if err := os.WriteFile(filepath.Join(generation, "settings.json"), []byte(`{"mcpServers":{"remote":{"url":"https://tools.example/mcp"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessAntigravity, ExtraArgs: []string{"--add-dir", generation}}
	servers, err := acpMCPServers(p, nil)
	if err != nil || len(servers) != 1 || servers[0].URL != "https://tools.example/mcp" {
		t.Fatalf("remote catalog server: %+v, %v", servers, err)
	}
}

func TestAntigravityACPGeneratedWrapperReceivesOnlyItsAliases(t *testing.T) {
	generation := t.TempDir()
	settings := `{"mcpServers":{"private":{"command":"/private/dyna","args":["mcp","serve","GENERATION","private"]},"other":{"command":"/tools","args":[]}}}`
	settings = strings.ReplaceAll(settings, "GENERATION", generation)
	if err := os.WriteFile(filepath.Join(generation, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	alias := harnessconfig.MCPEnvironmentName("OPENAI_API_KEY")
	specs := map[string]harnessconfig.MCPExecSpec{"private": {Command: "/provider-tools", Env: map[string]string{"OPENAI_API_KEY": alias}}}
	raw, err := json.Marshal(specs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "mcp-exec.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessAntigravity, ExtraArgs: []string{"--add-dir", generation}}
	servers, err := acpMCPServers(p, []string{alias + "=fixture-token", "DYNA_MCP_UNRELATED=other-token", "GITHUB_TOKEN=unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Name != "other" || len(servers[0].Env) != 0 || servers[1].Name != "private" || len(servers[1].Env) != 1 || servers[1].Env[0].Name != alias || servers[1].Env[0].Value != "fixture-token" {
		t.Fatalf("wrong alias scope: %+v", servers)
	}
	if _, err := acpMCPServers(p, nil); err == nil {
		t.Fatal("missing wrapper alias was accepted")
	}
}
