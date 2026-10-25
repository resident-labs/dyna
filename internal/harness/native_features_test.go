package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

func TestNativeFeaturePolicyInitialAndResume(t *testing.T) {
	for _, tc := range []struct {
		harness string
		option  string
		tools   []string
	}{
		{profile.HarnessClaudeCode, "--disallowedTools", []string{"Agent", "Workflow", "SendMessage", "ListAgents", "TeamCreate", "TeamDelete", "CronCreate", "CronList", "CronDelete"}},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			inv, err := buildInvocation(profile.Profile{Harness: tc.harness}, "task")
			if err != nil {
				t.Fatal(err)
			}
			if inv.cleanup != nil {
				defer inv.cleanup()
			}
			if inv.resume == nil {
				t.Fatal("missing exact-session resume")
			}
			for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
				for _, tool := range tc.tools {
					if !hasOptionValue(spec.argv, tc.option, tool) {
						t.Errorf("missing %s denial in %v", tool, spec.argv)
					}
				}
				if tc.harness == profile.HarnessClaudeCode && !hasArg(spec.argv, "--dangerously-skip-permissions") {
					t.Error("Dyna's own tool denials changed automatic permission mode")
				}
			}
		})
	}
}

func TestNativeFeaturePolicyPreservesExplicitClaudePermissions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  []string
		bypass bool
	}{
		{name: "autonomous", bypass: true},
		{name: "Agent", extra: []string{"--disallowedTools", "Agent"}},
		{name: "CronCreate", extra: []string{"--disallowed-tools=CronCreate"}},
	} {
		for _, prepared := range []bool{false, true} {
			name := tc.name + "/direct"
			extra := tc.extra
			var permissionArgs []string
			if prepared {
				name = tc.name + "/prepared"
				permissionArgs = append([]string{}, tc.extra...)
				extra = harnessruntime.NativeFeatureArgs(profile.HarnessClaudeCode, tc.extra, false)
			}
			t.Run(name, func(t *testing.T) {
				inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode, ExtraArgs: extra, RuntimePermissionArgs: permissionArgs}, "task")
				if err != nil {
					t.Fatal(err)
				}
				if inv.resume == nil {
					t.Fatal("missing exact-session resume")
				}
				for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
					if got := hasArg(spec.argv, "--dangerously-skip-permissions"); got != tc.bypass {
						t.Errorf("permission bypass = %v, want %v: %v", got, tc.bypass, spec.argv)
					}
					for _, tool := range []string{"Agent", "CronCreate"} {
						if !hasOptionValue(spec.argv, "--disallowedTools", tool) {
							t.Errorf("lost %s denial: %v", tool, spec.argv)
						}
					}
				}
			})
		}
	}
}

func TestDynaShellRemovesNativeShellOnlyForSessions(t *testing.T) {
	for _, tc := range []struct {
		h     string
		flag  string
		shell []string
		extra []string
		keep  []string
	}{
		{profile.HarnessClaudeCode, "--disallowedTools", []string{"Bash", "GetTask", "TaskOutput", "TaskStop", "Monitor"}, nil, []string{"Agent"}},
		{profile.HarnessPi, "--exclude-tools", []string{"bash"}, []string{"--exclude-tools", "find"}, []string{"find"}},
		// Muse has no tool denylist: its Shell entries are the flags that
		// remove both native shell tools.
		{profile.HarnessMuse, "", []string{"--disable-shell"}, nil, nil},
	} {
		t.Run(tc.h, func(t *testing.T) {
			for _, dynaShell := range []bool{true, false} {
				inv, err := buildInvocationWithExecution(profile.Profile{Harness: tc.h, ExtraArgs: tc.extra}, "task", ExecutionOptions{DynaShell: dynaShell})
				if err != nil {
					t.Fatal(err)
				}
				if inv.cleanup != nil {
					defer inv.cleanup()
				}
				for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
					for _, tool := range tc.shell {
						got := hasOptionValue(spec.argv, tc.flag, tool)
						if tc.flag == "" {
							got = hasArg(spec.argv, tool)
						}
						if got != dynaShell {
							t.Errorf("DynaShell=%v: %s denied=%v in %v", dynaShell, tool, got, spec.argv)
						}
					}
					for _, tool := range tc.keep {
						if !hasOptionValue(spec.argv, tc.flag, tool) {
							t.Errorf("DynaShell=%v lost %s: %v", dynaShell, tool, spec.argv)
						}
					}
				}
			}
		})
	}
}

// Muse gives a stdio MCP server only a small default environment, and Dyna's
// shell jobs run with the shell server's own launch environment. The launch
// completes the generated server pair with the whole final environment, so a
// job keeps the session scratch, the prepared tool roots and the GitHub
// configuration like the native shell had them. Workflow workers need the
// same server environment even though they keep the native shell enabled.
func TestMuseShellServerGetsTheFinalLaunchEnvironment(t *testing.T) {
	t.Setenv("DYNA_TEST_INHERITED", "1")
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"mcpServers":{"dyna":{"command":"/fake/dyna","args":["mcp","shell"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	adapter, err := harnessruntime.Adapter("muse")
	if err != nil {
		t.Fatal(err)
	}
	p := profile.Profile{Harness: profile.HarnessMuse, RuntimeExecutable: "/bin/muse", ExtraArgs: adapter.ConfigArgs(t.TempDir(), settings, ""), Env: map[string]string{
		harnesshome.OwnerEnvKey: `{"scope":"/fake/.runtime","env":{"HOME":"/owner","DYNA_BIN":"/fake/dyna"}}`,
		"DYNA_BIN":              "/fake/dyna",
		"DYNA_SCRATCH":          "/scratch-canary",
		"GOPATH":                "/gopath-canary",
		"GOMODCACHE":            "/gomodcache-canary",
		"GOCACHE":               "/gocache-canary",
		"CARGO_HOME":            "/cargohome-canary",
		"GH_CONFIG_DIR":         "/ghconfig-canary",
		"GH_TOKEN":              "gh-token-canary",
	}}
	for _, dynaShell := range []bool{true, false} {
		inv, err := buildInvocationWithExecution(p, "task", ExecutionOptions{DynaShell: dynaShell})
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
			cmd, err := harnessCommand(context.Background(), p, t.TempDir(), spec.argv, ExecutionOptions{DynaShell: dynaShell, SkipComputer: true})
			if err != nil {
				t.Fatal(err)
			}
			env, pairs := museDynaServerEnv(cmd.Args)
			if pairs != 1 {
				t.Fatalf("want one mcpServers.dyna pair, got %d in %v", pairs, cmd.Args)
			}
			if cmd.Args[len(cmd.Args)-1] != spec.argv[len(spec.argv)-1] {
				t.Errorf("prompt argument is not last: %v", cmd.Args)
			}
			for _, name := range []string{harnesshome.OwnerEnvKey, "DYNA_BIN"} {
				if !slices.Contains(cmd.Env, name+"="+p.Env[name]) {
					t.Errorf("DynaShell=%v: %s missing from the Muse launch environment", dynaShell, name)
				}
			}
			// Every identifier of the final launch environment reaches the
			// server; only identifiers can be referenced in the env map.
			for _, pair := range cmd.Env {
				name, _, _ := strings.Cut(pair, "=")
				if name == "" || strings.ContainsFunc(name, func(r rune) bool {
					return r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
				}) {
					continue
				}
				if env[name] != "${"+name+"}" {
					t.Errorf("%s = %q, want ${%s}", name, env[name], name)
				}
			}
			for _, name := range []string{harnesshome.OwnerEnvKey, "DYNA_BIN", "DYNA_SCRATCH", "GOPATH", "GOMODCACHE", "GOCACHE", "CARGO_HOME", "GH_CONFIG_DIR", "GH_TOKEN", "DYNA_TEST_INHERITED"} {
				if env[name] != "${"+name+"}" {
					t.Errorf("%s missing from the server environment: %v", name, env)
				}
			}
		}
	}
}

// museDynaServerEnv returns the env map of the launched "mcpServers.dyna" pair
// and how many such pairs the command line carries.
func museDynaServerEnv(args []string) (map[string]string, int) {
	env := map[string]string{}
	pairs := 0
	for _, arg := range args {
		raw, ok := strings.CutPrefix(arg, "mcpServers.dyna=")
		if !ok {
			continue
		}
		pairs++
		var entry struct {
			Env map[string]string `json:"env"`
		}
		if json.Unmarshal([]byte(raw), &entry) != nil {
			continue
		}
		env = entry.Env
	}
	return env, pairs
}

// A keeper or a generation without Dyna's server must not acquire an env-only
// server table: Muse rejects a server entry that defines no transport.
func TestShellEnvironmentDoesNotCreateMissingServer(t *testing.T) {
	for _, h := range []string{profile.HarnessMuse} {
		for _, dynaShell := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/DynaShell=%v", h, dynaShell), func(t *testing.T) {
				extra := []string{"-c", `mcpServers.other={"command":"/fake/other"}`}
				p := profile.Profile{Harness: h, RuntimeExecutable: "/bin/" + h, ExtraArgs: extra}
				inv, err := buildInvocationWithExecution(p, "task", ExecutionOptions{DynaShell: dynaShell})
				if err != nil {
					t.Fatal(err)
				}
				for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
					cmd, err := harnessCommand(context.Background(), p, t.TempDir(), spec.argv, ExecutionOptions{DynaShell: dynaShell, SkipComputer: true})
					if err != nil {
						t.Fatal(err)
					}
					for _, pair := range configArgs(cmd.Args) {
						if strings.HasPrefix(pair, "mcp_servers.dyna.") || strings.HasPrefix(pair, "mcpServers.dyna=") {
							t.Fatalf("created a Dyna server without a transport: %s", pair)
						}
					}
					if !slices.Contains(cmd.Args, extra[1]) {
						t.Fatal("lost the other server")
					}
				}
			})
		}
	}
}

// A Pi session runs with extension discovery off and with Dyna's MCP shell.
// Pi implements MCP support as the builtin:mcp extension (shipped
// docs/cli.md), so the session loads that one alone: it keeps every MCP tool
// and still loses the native bash tool.
func TestPiSessionKeepsMCPWhileExcludingNativeBash(t *testing.T) {
	inv, err := buildInvocationWithExecution(profile.Profile{Harness: profile.HarnessPi}, "task", ExecutionOptions{DisableNativeSubagents: true, DynaShell: true})
	if err != nil {
		t.Fatal(err)
	}
	if inv.cleanup != nil {
		defer inv.cleanup()
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
		if !hasArg(spec.argv, "--no-extensions") {
			t.Errorf("extension discovery is on: %v", spec.argv)
		}
		if !hasOptionValue(spec.argv, "-e", "builtin:mcp") {
			t.Errorf("MCP support is not loaded: %v", spec.argv)
		}
		if !hasOptionValue(spec.argv, "--exclude-tools", "bash") {
			t.Errorf("native bash is not excluded: %v", spec.argv)
		}
	}
}

// Catalog MCP servers keep Pi's default codemode exposure, and Pi reaches
// those tools only through builtin:codemode. Without it they connect but
// cannot be called (D-99).
func TestPiSessionLoadsCodemodeForCatalogMCPTools(t *testing.T) {
	inv, err := buildInvocationWithExecution(profile.Profile{Harness: profile.HarnessPi}, "task", ExecutionOptions{DisableNativeSubagents: true, DynaShell: true})
	if err != nil {
		t.Fatal(err)
	}
	if inv.cleanup != nil {
		defer inv.cleanup()
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("session-id", "next")} {
		if !hasOptionValue(spec.argv, "-e", "builtin:codemode") {
			t.Errorf("catalog MCP tools are unreachable: %v", spec.argv)
		}
		if !hasOptionValue(spec.argv, "-e", "builtin:mcp") {
			t.Errorf("MCP support is not loaded: %v", spec.argv)
		}
	}
}
