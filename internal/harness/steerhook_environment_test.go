package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
)

func TestMuseSteerHookForwardsOwnerEnvironment(t *testing.T) {
	roots := harnesshome.Roots(filepath.Join(t.TempDir(), ".runtime"))
	roots[harnesshome.OwnerEnvKey] = `{"scope":"/fake/.runtime","env":{"HOME":"/owner","DYNA_BIN":"/fake/dyna"}}`
	roots["DYNA_SESSION"] = "session-canary"
	roots["ANTHROPIC_API_KEY"] = "provider-secret-canary"
	p := profile.Profile{Harness: profile.HarnessMuse, RuntimeExecutable: "/bin/false", Env: roots, ExtraArgs: []string{"-c", `mcpServers.dyna={"command":"/fake/dyna","args":["mcp","shell"]}`}}
	inv, err := buildInvocation(p, "task")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := nativeSessionLaunch(p, t.TempDir(), inv.initial, ExecutionOptions{SkipComputer: true})
	if err != nil {
		t.Fatal(err)
	}
	args, err := museSessionHookArgs(launch.Key, launch.Argv, launch.Env)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(strings.Join(args, "\n"))
	var names []string
	var hookPath string
	for _, pair := range codexConfigArgs(args) {
		if raw, ok := strings.CutPrefix(pair, "managed_hooks_env_vars="); ok {
			if err := json.Unmarshal([]byte(raw), &names); err != nil {
				t.Fatal(err)
			}
		}
		if raw, ok := strings.CutPrefix(pair, "managed_hooks_path="); ok {
			if err := json.Unmarshal([]byte(raw), &hookPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if hookPath == "" {
		t.Fatal("Muse hook is not managed; its environment will be cleared")
	}
	for key := range roots {
		if key == "ANTHROPIC_API_KEY" {
			continue
		}
		if !slices.Contains(names, key) {
			t.Errorf("hook environment lost %s", key)
		}
	}
	if slices.Contains(names, "ANTHROPIC_API_KEY") || strings.Contains(string(data), "provider-secret-canary") {
		t.Fatal("managed hook requests a forbidden provider credential")
	}
	hooks, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(hooks, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Hooks["PreToolUse"]) == 0 {
		t.Fatal("managed file lost memory denial")
	}
	if len(config.Hooks["PostToolUse"]) != 0 {
		t.Fatal("MSP steering must not install a per-turn hook")
	}
}
