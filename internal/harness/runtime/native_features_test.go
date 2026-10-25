package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/opencodefixture"
	"github.com/pelletier/go-toml/v2"
)

func TestOpenCodeGenerationAllowsManagedSkillsAfterDefaultDeny(t *testing.T) {
	r, home, id := generationFixture(t, "opencode")
	addGenerationSkill(t, r, "opencode")
	g := buildGeneration(t, r, "opencode", home, id)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Policies []struct{ Action, Resource, Effect string }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var skills []string
	for _, policy := range doc.Policies {
		if policy.Action == "integration.use" {
			skills = append(skills, policy.Resource+"="+policy.Effect)
		}
	}
	want := []string{"skill:*=deny", "skill:base=allow"}
	if !reflect.DeepEqual(skills, want) {
		t.Fatalf("skill policy = %v, want %v", skills, want)
	}
	if g.Env["OPENCODE_CONFIG_PROJECT_DISABLE"] != "" {
		t.Fatal("project discovery disable would drop repository instructions")
	}
}

// The local provider records the actual model request without credentials or
// external traffic. Enable with the installed, pinned OpenCode bundle.
func TestOpenCodeRealAncestorSkillsPreserveInstructions(t *testing.T) {
	exe := os.Getenv("OPENCODE_INTEGRATION_BINARY")
	if exe == "" {
		t.Skip("set OPENCODE_INTEGRATION_BINARY to the private v2 bundle")
	}
	r, home, id := generationFixture(t, "opencode")
	root := t.TempDir()
	repo := filepath.Join(root, "owner", "repo")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".agents", ".claude"} {
		name := strings.TrimPrefix(dir, ".") + "-ancestor-canary"
		write(filepath.Join(root, "owner", dir, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Owner ancestor canary.\n---\nOwner skill.\n")
	}
	for _, name := range []string{"opencode", "report"} {
		write(filepath.Join(root, "owner", ".agents", "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: OWNER_BUILTIN_SHADOW_CANARY\n---\nOWNER_BUILTIN_SHADOW_CONTENT\n")
	}
	write(filepath.Join(repo, "AGENTS.md"), "REPOSITORY_INSTRUCTION_CANARY\n")
	write(filepath.Join(root, "owner", ".opencode", "opencode.json"), `{"policies":[{"action":"integration.use","resource":"skill:*","effect":"allow"}]}`)
	managed := filepath.Join(root, "managed")
	write(filepath.Join(managed, "SKILL.md"), "---\nname: managed-canary\ndescription: Dyna managed canary.\n---\nManaged skill.\n")
	if err := r.Config.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessmgr.ManagedSkill{Name: "managed-canary", Source: managed, EnabledHarnesses: []string{"opencode"}}); err != nil {
		t.Fatal(err)
	}
	g := buildGeneration(t, r, "opencode", home, id)
	config := filepath.Join(g.Directory, "settings.json")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fixture := opencodefixture.New(t, doc, nil)
	env := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "PWD=" + repo}
	for key, value := range fixture.Env {
		env = append(env, key+"="+value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Canary", "-c", "user.email=canary@example.test", "commit", "--allow-empty", "-qm", "Canary"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %s (%v)", output, err)
		}
	}
	cmd := exec.CommandContext(ctx, exe, "run", "--format", "json", "--model", "canary/probe", "DYNA_INJECTED_INSTRUCTION_CANARY")
	cmd.Env, cmd.Dir = env, repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local OpenCode turn: %s (%v)", output, err)
	}
	for _, want := range []string{"REPOSITORY_INSTRUCTION_CANARY", "DYNA_INJECTED_INSTRUCTION_CANARY", "managed-canary"} {
		if !strings.Contains(fixture.Requests(), want) {
			t.Errorf("model request lost %s", want)
		}
	}
	for _, denied := range []string{"agents-ancestor-canary", "claude-ancestor-canary", "OWNER_BUILTIN_SHADOW_CANARY", "OWNER_BUILTIN_SHADOW_CONTENT"} {
		if strings.Contains(fixture.Requests(), denied) {
			t.Errorf("model request includes owner skill %s", denied)
		}
	}
}

func TestNativeFeatureArgumentsAreIdempotentAndPreserveUserDenials(t *testing.T) {
	for _, tc := range []struct {
		h         string
		args      []string
		dynaShell bool
	}{
		{"claude-code", []string{"--disallowedTools", "Bash(git *)", "Agent(reviewer)", "--disallowed-tools=WebFetch"}, true},
		{"pi", []string{"-xt", "find,extra", "--exclude-tools", "extra"}, true},
		{"muse", []string{"--disable-approval", "--disable-shell"}, true},
	} {
		t.Run(tc.h, func(t *testing.T) {
			once := NativeFeatureArgs(tc.h, tc.args, tc.dynaShell)
			if twice := NativeFeatureArgs(tc.h, once, tc.dynaShell); !reflect.DeepEqual(once, twice) {
				t.Fatalf("policy is not idempotent: %v then %v", once, twice)
			}
			joined := strings.Join(once, " ")
			for _, want := range map[string][]string{"claude-code": {"Bash(git *)", "Agent(reviewer)", "WebFetch"}, "pi": {"--exclude-tools find,extra,bash"}, "muse": {"--disable-approval --disable-request-user-input --no-foreign-personal-context --disable-shell"}}[tc.h] {
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

func TestNativeFeatureGapsAreReported(t *testing.T) {
	for _, c := range Capabilities() {
		if c.Harness == "muse" {
			if c.Status != "partial" || !strings.Contains(c.Reason, "No switch verified") {
				t.Errorf("unreported feature gap: %+v", c)
			}
		}
	}
}

// Muse stays partial for startup memory loading, which has no verified switch,
// and for Muse versions before 1.4.7, which cannot load the memory denial hook.
func TestMuseKeepsMemoryGaps(t *testing.T) {
	for _, c := range Capabilities() {
		if c.Harness != "muse" {
			continue
		}
		for _, want := range []string{"startup memory", "before 1.4.7"} {
			if c.Status != "partial" || !strings.Contains(c.Reason, want) {
				t.Errorf("Muse lacks the %q gap: %+v", want, c)
			}
		}
		return
	}
	t.Fatal("Muse is missing from the capabilities")
}

func TestNativeFeaturesInFreshAndRestoredGenerations(t *testing.T) {
	for _, h := range []string{"claude-code", "pi", "opencode", "muse"} {
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
				case "muse":
					for _, flag := range []string{"--disable-request-user-input", "--no-foreign-personal-context"} {
						if !strings.Contains(joined, flag) {
							t.Errorf("%s lacks %s", g.name, flag)
						}
					}
					for _, want := range []string{`local_session_messaging={"enabled":false}`, `run.workflow_trigger_mode="off"`, `run.subagent_delegation_mode="off"`, `agents={"execution_capacity":1}`, `plugin:goal-reminder:reminder:goal-reminder`} {
						if !strings.Contains(joined, want) {
							t.Errorf("%s lacks %s: %s", g.name, want, joined)
						}
					}
				}
			}
			if !reflect.DeepEqual(fresh.Args, restored.Args) {
				t.Fatal("restored policy arguments differ from fresh arguments")
			}
			if h == "claude-code" || h == "opencode" || h == "muse" {
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
				case "opencode":
					want := []any{
						map[string]any{"action": "tool.use", "resource": "subagent:*", "effect": "deny"},
						map[string]any{"action": "tool.use", "resource": "question:*", "effect": "deny"},
						map[string]any{"action": "integration.use", "resource": "skill:*", "effect": "deny"},
					}
					if !reflect.DeepEqual(doc["policies"], want) {
						t.Errorf("hard policies = %#v, want %#v", doc["policies"], want)
					}
				case "muse":
					caps, _ := doc["runtime_capabilities"].(map[string]any)
					if len(caps) != 6 {
						t.Errorf("observer capability count = %d, want 6", len(caps))
					}
					for name, value := range caps {
						if !reflect.DeepEqual(value, map[string]any{"enabled": false}) {
							t.Errorf("observer %s = %#v", name, value)
						}
					}
				}
			}
		})
	}
}

func TestOpenCodeRealServicePoliciesBlockTools(t *testing.T) {
	for _, name := range []string{"question", "subagent"} {
		t.Run(name, func(t *testing.T) {
			r, home, id := generationFixture(t, "opencode")
			generation := buildGeneration(t, r, "opencode", home, id)
			raw, err := os.ReadFile(filepath.Join(generation.Directory, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err = json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			fixture := opencodefixture.New(t, doc, func(body map[string]any) string {
				tools, _ := body["tools"].([]any)
				if len(tools) == 0 {
					return ""
				}
				messages, _ := body["messages"].([]any)
				for _, value := range messages {
					message, _ := value.(map[string]any)
					if message["role"] == "tool" {
						return ""
					}
				}
				if name == "question" {
					return opencodefixture.Tool(name, map[string]any{"questions": []any{map[string]any{"question": "QUESTION_CANARY", "header": "Probe", "options": []any{map[string]any{"label": "ok", "description": "Canary"}}}}})
				}
				return opencodefixture.Tool(name, map[string]any{"description": "SUBAGENT_CANARY", "prompt": "SUBAGENT_CANARY", "agent": "explore", "subagent_type": "explore"})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, fixture.Binary, "run", "--format", "json", "--model", "canary/probe", "--auto", "POLICY_CALL_CANARY")
			cmd.Env = []string{}
			for key, value := range fixture.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("policy call: %s %v", output, err)
			}
			if !strings.Contains(fixture.Last(), "Blocked by configuration policy") {
				t.Errorf("%s was not denied by the service; output=%s", name, output)
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
		// Muse removes its shell tools with a flag, and its generated args
		// name Dyna's own shell ("mcp shell"), so only bash_input can be
		// searched here. The argv checks below cover the flag.
		{"muse", "settings.json", []string{"bash_input"}, true, ""},
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

func TestOpenCodeGenerationDeniesNativeShellAndCarriesDynaServer(t *testing.T) {
	r, home, id := generationFixture(t, "opencode")
	g := buildGeneration(t, r, "opencode", home, id)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Permission map[string]any `json:"permission"`
		MCP        struct {
			Servers map[string]struct {
				Type     string   `json:"type"`
				Command  []string `json:"command"`
				Codemode *bool    `json:"codemode"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Permission["shell"] != "deny" {
		t.Errorf("native shell is not denied: %s", raw)
	}
	got := doc.MCP.Servers["dyna"]
	if got.Type != "local" || len(got.Command) != 3 || !filepath.IsAbs(got.Command[0]) || strings.Join(got.Command[1:], " ") != "mcp shell" || got.Codemode == nil || *got.Codemode {
		t.Errorf("dyna server = %+v in %s", got, raw)
	}
}

// The one -c pair per server lowering carries Dyna's shell server with its
// command and arguments. The launch adds the environment it needs (see
// TestMuseShellServerGetsTheFinalLaunchEnvironment).
func TestMuseLoweringCarriesTheDynaServerPair(t *testing.T) {
	r, home, id := generationFixture(t, "muse")
	g := buildGeneration(t, r, "muse", home, id)
	var entry struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	found := false
	for i, arg := range g.Args {
		if arg != "-c" || i+1 >= len(g.Args) {
			continue
		}
		pair, ok := strings.CutPrefix(g.Args[i+1], "mcpServers."+dynaMCPName+"=")
		if !ok {
			continue
		}
		found = true
		if json.Unmarshal([]byte(pair), &entry) != nil {
			t.Fatalf("dyna server pair is not JSON: %s", pair)
		}
	}
	if !found {
		t.Fatalf("the -c pairs carry no dyna server: %v", g.Args)
	}
	if !filepath.IsAbs(entry.Command) || filepath.Base(entry.Command) != "dyna" || strings.Join(entry.Args, " ") != "mcp shell" {
		t.Fatalf("dyna server = %q %v", entry.Command, entry.Args)
	}
	if len(entry.Env) != 0 {
		t.Fatalf("the generation lists the launch environment: %v", entry.Env)
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
