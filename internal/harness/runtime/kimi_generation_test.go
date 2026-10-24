package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestKimiGenerationRejectsLinkedConfig(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	settings := `{"mcpServers":{},"tools":{"disabled":["Agent"]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.toml")
	if err := os.WriteFile(external, []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := publishKimiGeneration(dir, home); err == nil {
		t.Fatal("a linked native config was accepted")
	}
}

func TestKimiGenerationMergesOnlyToolPolicy(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	settings := `{"mcpServers":{"dyna":{"command":"/private/dyna","args":["mcp","shell"]}},"tools":{"disabled":["Agent","Bash","TaskList"]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	config := []byte(`default_model = "kimi-code/k3"
custom_future_field = "preserve"

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.ai/coding/v1"

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code-env-0123456789abcdef"

[tools]
disabled = ["Read"]
`)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishKimiGeneration(dir, home); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		DefaultModel string `toml:"default_model"`
		Custom       string `toml:"custom_future_field"`
		Providers    map[string]struct {
			BaseURL string `toml:"base_url"`
			OAuth   struct {
				Key string `toml:"key"`
			} `toml:"oauth"`
		} `toml:"providers"`
		Tools struct {
			Disabled []string `toml:"disabled"`
		} `toml:"tools"`
	}
	if err := toml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultModel != "kimi-code/k3" || got.Custom != "preserve" || got.Providers["managed:kimi-code"].BaseURL != "https://api.kimi.ai/coding/v1" || got.Providers["managed:kimi-code"].OAuth.Key != "oauth/kimi-code-env-0123456789abcdef" {
		t.Fatalf("managed config changed: %+v", got)
	}
	for _, name := range []string{"Read", "Agent", "Bash", "TaskList"} {
		if !slices.Contains(got.Tools.Disabled, name) {
			t.Errorf("tools.disabled lacks %s: %v", name, got.Tools.Disabled)
		}
	}
}
