package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestNativeFeatureArgumentsAreIdempotentAndPreserveUserDenials(t *testing.T) {
	for _, tc := range []struct {
		h         string
		args      []string
		dynaShell bool
	}{
		{"claude-code", []string{"--disallowedTools", "Bash(git *)", "Agent(reviewer)", "--disallowed-tools=WebFetch"}, true},
		{"pi", []string{"-xt", "find,extra", "--exclude-tools", "extra"}, true},
	} {
		t.Run(tc.h, func(t *testing.T) {
			once := NativeFeatureArgs(tc.h, tc.args, tc.dynaShell)
			if twice := NativeFeatureArgs(tc.h, once, tc.dynaShell); !reflect.DeepEqual(once, twice) {
				t.Fatalf("policy is not idempotent: %v then %v", once, twice)
			}
			joined := strings.Join(once, " ")
			for _, want := range map[string][]string{"claude-code": {"Bash(git *)", "Agent(reviewer)", "WebFetch"}, "pi": {"--exclude-tools find,extra,bash"}}[tc.h] {
				if !strings.Contains(joined, want) {
					t.Errorf("lost %s: %s", want, joined)
				}
			}
		})
	}
}

func TestNativeFeaturePolicyUpgradesPinnedGeneration(t *testing.T) {
	r, p := runtimeFixture(t, "claude-code")
	current := nativeFeaturePolicies["claude-code"]
	t.Cleanup(func() { nativeFeaturePolicies["claude-code"] = current })
	nativeFeaturePolicies["claude-code"] = nativeFeaturePolicy{}
	_, old, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeFeaturePolicies["claude-code"] = current
	_, next, err := r.Prepare(context.Background(), p, &old)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation == old.Generation || !currentNativeFeaturePolicy(next.Generation, "claude-code") {
		t.Fatal("resume kept the generation without the current feature policy")
	}
	raw, err := os.ReadFile(filepath.Join(next.Generation, "settings.json"))
	if err != nil || !strings.Contains(string(raw), `"autoMemoryEnabled": false`) {
		t.Fatalf("upgraded config lacks memory policy: %s (%v)", raw, err)
	}
}

func TestNativeFeatureEnvironmentOverridesProfileOnResume(t *testing.T) {
	r, p := runtimeFixture(t, "claude-code")
	p.Env = map[string]string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY": "0", "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"}
	first, pin, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := r.Prepare(context.Background(), p, &pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []map[string]string{first.Env, next.Env} {
		if env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "1" || env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] != "0" {
			t.Fatal("profile overrode forced native controls")
		}
	}
}

func TestNativeFeaturesInFreshAndRestoredGenerations(t *testing.T) {
	for _, h := range []string{"claude-code", "pi"} {
		t.Run(h, func(t *testing.T) {
			r, home, id := generationFixture(t, h)
			fresh := buildGeneration(t, r, h, home, id)
			restored, err := loadNativeGeneration(h, fresh.Directory, os.LookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range []struct {
				name string
				args []string
				env  map[string]string
			}{{"fresh", fresh.Args, fresh.Env}, {"restored", restored.Args, restored.Env}} {
				joined := strings.Join(g.args, " ")
				switch h {
				case "claude-code":
					for _, key := range []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY", "CLAUDE_CODE_DISABLE_CRON", "CLAUDE_CODE_DISABLE_WORKFLOWS", "CLAUDE_CODE_DISABLE_AGENT_VIEW"} {
						if g.env[key] != "1" {
							t.Errorf("%s %s=%q, want 1", g.name, key, g.env[key])
						}
					}
					if g.env["CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"] != "0" {
						t.Errorf("%s enables agent teams", g.name)
					}
				case "pi":
					if strings.Contains(joined, "--no-extensions") {
						t.Error("policy added an unrelated extension restriction")
					}
				}
			}
			if !reflect.DeepEqual(fresh.Args, restored.Args) {
				t.Fatal("restored policy arguments differ from fresh arguments")
			}
			if h == "claude-code" {
				raw, err := os.ReadFile(filepath.Join(fresh.Directory, "settings.json"))
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(raw, &doc); err != nil {
					t.Fatal(err)
				}
				switch h {
				case "claude-code":
					for _, tool := range []string{"ScheduleWakeup", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "EnterWorktree", "ExitWorktree", "TodoWrite"} {
						if !strings.Contains(string(raw), `"`+tool+`"`) || !strings.Contains(strings.Join(fresh.Args, " "), tool) {
							t.Errorf("generated launch permits native %s outside Dyna", tool)
						}
					}
					if doc["autoMemoryEnabled"] != false {
						t.Error("generated settings do not disable auto memory")
					}
					if !strings.Contains(string(raw), `"SendMessage"`) || !strings.Contains(string(raw), `"CronCreate"`) {
						t.Error("generated settings do not deny messaging and cron tools")
					}
				}
			}
		})
	}
}

// A DynaShell policy adds Dyna's MCP shell server to every generation of that
// harness. Generations are shared by every launch of an account, so they keep
// the native shell tools: only a session launch removes them.
func TestDynaShellServerJoinsDynaShellGenerations(t *testing.T) {
	for _, tc := range []struct {
		h, file string
		shell   []string
		wants   bool
		// exposure names the required server exposure field, when a harness
		// hides MCP tools from the model without it.
		exposure string
	}{
		{"claude-code", "mcp.json", []string{"Bash", "GetTask", "TaskOutput", "TaskStop", "Monitor"}, true, ""},
		{"pi", "settings.json", []string{"bash"}, true, "direct"},
	} {
		t.Run(tc.h, func(t *testing.T) {
			r, home, id := generationFixture(t, tc.h)
			g := buildGeneration(t, r, tc.h, home, id)
			raw, err := os.ReadFile(filepath.Join(g.Directory, tc.file))
			if err != nil {
				t.Fatal(err)
			}
			command, args, found := generationMCPServer(t, tc.file, raw, dynaMCPName)
			if found != tc.wants {
				t.Fatalf("dyna server present=%v, want %v in %s", found, tc.wants, raw)
			}
			if tc.wants && (!filepath.IsAbs(command) || filepath.Base(command) != "dyna" || strings.Join(args, " ") != "mcp shell") {
				t.Fatalf("dyna server = %q %v in %s", command, args, raw)
			}
			if tc.exposure != "" {
				var root struct {
					MCP map[string]struct {
						Exposure string `json:"exposure"`
					} `json:"mcpServers"`
				}
				if err := json.Unmarshal(raw, &root); err != nil {
					t.Fatal(err)
				}
				if root.MCP[dynaMCPName].Exposure != tc.exposure {
					t.Fatalf("dyna server exposure = %q, want %q in %s", root.MCP[dynaMCPName].Exposure, tc.exposure, raw)
				}
			}
			// The account-wide generation keeps the native shell tools: only a
			// session launch removes them, and a policy with no switch yet
			// removes nothing. Grok's shared agent process cannot take per-launch
			// argv, so its generation denies the native shell in the same config
			// that adds Dyna's shell server. Kimi merges the same denylist into
			// its native config because ACP has no per-session tool filter.
			if false {
				for _, tool := range tc.shell {
					if !strings.Contains(string(raw), tool) {
						t.Fatalf("grok generation lacks the shell deny hook entry %s: %s", tool, raw)
					}
				}
				return
			}
			if sh := nativeFeaturePolicies[tc.h].Shell; len(sh) != 0 && slices.Equal(NativeFeatureArgs(tc.h, g.Args, true), g.Args) {
				t.Fatalf("a session launch removes nothing: %v", g.Args)
			}
			if kept := NativeFeatureArgs(tc.h, g.Args, false); !slices.Equal(kept, g.Args) {
				t.Fatalf("the generation removes the native shell: %v then %v", g.Args, kept)
			}
			joined := strings.Join(append(append([]string{}, g.Args...), string(raw)), " ")
			for _, tool := range tc.shell {
				if strings.Contains(joined, tool) {
					t.Fatalf("generation removes the native shell tool %s: %s %v", tool, raw, g.Args)
				}
			}
		})
	}
}

// generationMCPServer reads one generated MCP server entry out of a native
// config file in either generated format.
func generationMCPServer(t *testing.T, file string, raw []byte, name string) (string, []string, bool) {
	t.Helper()
	var root map[string]any
	var err error
	if strings.HasSuffix(file, ".toml") {
		err = toml.Unmarshal(raw, &root)
	} else {
		err = json.Unmarshal(raw, &root)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mcpServers", "mcp_servers"} {
		servers, ok := root[key].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := servers[name].(map[string]any)
		if !ok {
			continue
		}
		command, _ := entry["command"].(string)
		var args []string
		if listed, ok := entry["args"].([]any); ok {
			for _, arg := range listed {
				args = append(args, fmt.Sprint(arg))
			}
		}
		return command, args, true
	}
	return "", nil, false
}
