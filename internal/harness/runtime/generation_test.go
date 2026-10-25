package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/store"
)

func TestNativePluginGenerationsCarryOnlyCatalogResources(t *testing.T) {
	for _, h := range []string{"opencode", "pi", "grok", "muse"} {
		t.Run(h, func(t *testing.T) {
			accounts := testAccounts(t)
			t.Setenv("DYNA_BIN", "/fake/dyna")
			n, err := accounts.Create(h, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			home, err := accounts.HarnessHome(h)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			r := &Runtime{Accounts: accounts, Config: config.NewConfigManager(), Generations: filepath.Join(store.DataDir(), "harnesses", "generations")}
			t.Cleanup(func() { _ = removeBundle(r.Generations) })
			if err := r.Config.AddMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), config.ManagedMCP{Name: "tools", MCPDefinition: config.MCPDefinition{Command: "node", Args: []string{"tools.js"}, EnvKeys: []string{"MCP_TOKEN"}}, EnabledHarnesses: []string{h}}); err != nil {
				t.Fatal(err)
			}
			gen, err := r.generate(h, home, n.ID, func(key string) (string, bool) { return "secret-reference-canary", key == "MCP_TOKEN" })
			if err != nil {
				t.Fatal(err)
			}
			var target string
			switch h {
			case "pi":
				target = filepath.Join(home, "mcp.json")
			case "grok", "muse":
				target = filepath.Join(gen.Directory, "settings."+map[string]string{"grok": "toml", "muse": "json"}[h])
			case "opencode":
				target = filepath.Join(gen.Directory, "settings.json")
			}
			raw, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "tools") || strings.Contains(string(raw), "secret-reference-canary") {
				t.Fatal("native catalog payload missing or leaked secret")
			}
			metadata, err := os.ReadFile(filepath.Join(gen.Directory, "native-generation.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(metadata), "secret-reference-canary") {
				t.Fatal("secret entered manifest")
			}
			restored, err := loadNativeGeneration(h, gen.Directory, func(key string) (string, bool) { return "current-canary", key == "MCP_TOKEN" })
			if err != nil {
				t.Fatal(err)
			}
			if restored.Env["MCP_TOKEN"] != "current-canary" || strings.Join(restored.Args, "\x00") != strings.Join(gen.Args, "\x00") {
				t.Fatal("generation did not restore")
			}
			if h == "opencode" {
				var doc map[string]any
				if json.Unmarshal(raw, &doc) != nil {
					t.Fatal("invalid v2 JSON")
				}
				mcp := doc["mcp"].(map[string]any)
				if mcp["servers"] == nil || doc["update"] != "disable" {
					t.Fatal("incorrect OpenCode v2 schema")
				}
			}
		})
	}
}
