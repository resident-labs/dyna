package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestKimiGenerationDisablesConflictingNativeFeatures(t *testing.T) {
	r, home, id := generationFixture(t, "kimi-code")
	addGenerationMCP(t, r, "kimi-code", "other")
	g := buildGeneration(t, r, "kimi-code", home, id)
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		DefaultPlanMode       bool   `toml:"default_plan_mode"`
		DefaultPermissionMode string `toml:"default_permission_mode"`
		Telemetry             bool   `toml:"telemetry"`
		BuiltinProductSkills  bool   `toml:"builtin_product_skills"`
		Tools                 struct {
			Disabled []string `toml:"disabled"`
		} `toml:"tools"`
	}
	if err := toml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.DefaultPlanMode || document.DefaultPermissionMode != "auto" || document.Telemetry || document.BuiltinProductSkills {
		t.Fatalf("headless or built-in defaults are not disabled: %+v", document)
	}
	for _, name := range []string{"Agent", "AgentSwarm", "AskUserQuestion", "UpdateGoal", "TodoList", "EnterPlanMode", "ExitPlanMode", "WebSearch", "FetchURL", "Bash"} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("tools.disabled lacks %s", name)
		}
	}
	for _, name := range []string{"default_plan_mode", "default_permission_mode", "telemetry", "builtin_product_skills"} {
		if !strings.Contains(string(raw), name) {
			t.Errorf("generated config lacks %s", name)
		}
	}
	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	data, err := os.ReadFile(filepath.Join(home, "mcp.json"))
	if err != nil || json.Unmarshal(data, &mcp) != nil || len(mcp.Servers) != 0 {
		t.Fatalf("native MCP config is not empty: %s, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(home, "plugins", "installed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plugins struct {
		Version int      `json:"version"`
		Plugins []string `json:"plugins"`
	}
	if err := json.Unmarshal(data, &plugins); err != nil || plugins.Version != 1 || len(plugins.Plugins) != 0 {
		t.Fatalf("plugins are not disabled: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("unexpected global AGENTS.md: %v", err)
	}
	a, err := Adapter("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	env := a.NativeEnv(home)
	env["KIMI_CODE_BUILTIN_PRODUCT_SKILLS"] = "1"
	applyNativeFeatureEnvironment("kimi-code", env)
	if env["KIMI_CODE_TRUST_WORKSPACE"] != "0" || env["KIMI_CODE_NO_AUTO_UPDATE"] != "1" || env["KIMI_DISABLE_TELEMETRY"] != "1" || env["KIMI_CODE_BUILTIN_PRODUCT_SKILLS"] != "0" {
		t.Fatalf("isolation environment = %+v", env)
	}
	data, err = os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var generated struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &generated); err != nil {
		t.Fatal(err)
	}
	if len(generated.Servers) != 2 {
		t.Fatalf("generated MCP catalog lost an entry: %v", generated.Servers)
	}
	if _, ok := generated.Servers["dyna"]; !ok {
		t.Fatalf("Dyna shell MCP is missing: %v", generated.Servers)
	}
	if _, ok := generated.Servers["other"]; !ok {
		t.Fatalf("the ACP filtering fixture is missing its second server: %v", generated.Servers)
	}
}
