package harness

import (
	"testing"

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
