package runtime

import (
	accountstore "dyna-agent/internal/account/store"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/store"
)

func TestNativePluginGenerationsCarryOnlyCatalogResources(t *testing.T) {
	for _, h := range []string{"pi"} {
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
				target = filepath.Join(gen.Directory, "pi-mcp.json")
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
		})
	}
}

// A room MCP token lives only in the accounts store and the engine's request
// header. No generated harness file and no generation env may carry it or its URL.
func TestGenerationNeverCarriesRoomMCPToken(t *testing.T) {
	accounts := testAccounts(t)
	t.Setenv("DYNA_BIN", "/fake/dyna")
	n, err := accounts.Create("pi", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	const token, serverURL = "room-mcp-token-canary", "https://board.example.test/mcp"
	if err := accountstore.DefaultStore().Update(func(st *accountstore.State) error {
		_, err := st.SaveMCPBearer("room_1", "board", token)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: accounts, Config: config.NewConfigManager(), Generations: filepath.Join(store.DataDir(), "harnesses", "generations")}
	t.Cleanup(func() { _ = removeBundle(r.Generations) })
	gen, err := r.generate("pi", home, n.ID, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range gen.Env {
		if strings.Contains(key+value, token) || strings.Contains(key+value, serverURL) {
			t.Fatalf("generation env %s carries the room MCP token or URL", key)
		}
	}
	err = filepath.WalkDir(gen.Directory, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil && (strings.Contains(string(raw), token) || strings.Contains(string(raw), serverURL)) {
			t.Errorf("%s carries the room MCP token or URL", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
