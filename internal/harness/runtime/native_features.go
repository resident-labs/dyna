package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	harnessmgr "dyna-agent/internal/harness/config"
)

// Dyna owns memory, delegation, messaging, and session wakeups. Keep every
// verified native control here, including explicit gaps. Evidence applies to
// Claude 2.1.302, Grok 1.0.62, Pi 1.3.0 and Kimi Code 2.3.1.
type nativeFeaturePolicy struct {
	Env       map[string]string
	Config    map[string]any // dotted native setting paths
	Args      []string
	DenyFlags []string // canonical spelling first
	DenyTools []string
	NoSwitch  []string
	// DynaShell adds Dyna's MCP shell server to every generation. Shell
	// lists what a Dyna session loses for it: the native shell tools, or,
	// where a harness has no tool denylist, the flags that remove them.
	DynaShell bool
	Shell     []string
}

var nativeFeaturePolicies = map[string]nativeFeaturePolicy{
	"claude-code": {
		// code.claude.com/docs/en/env-vars; installed help and embedded JS.
		Env: map[string]string{
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY":      "1",
			"CLAUDE_CODE_DISABLE_CRON":             "1",
			"CLAUDE_CODE_DISABLE_WORKFLOWS":        "1",
			"CLAUDE_CODE_DISABLE_AGENT_VIEW":       "1",
			"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "0",
		},
		Config:    map[string]any{"autoMemoryEnabled": false},
		DenyFlags: []string{"--disallowedTools", "--disallowed-tools"},
		DenyTools: []string{"Agent", "Workflow", "SendMessage", "ListAgents", "TeamCreate", "TeamDelete", "CronCreate", "CronList", "CronDelete", "ScheduleWakeup", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "EnterWorktree", "ExitWorktree", "TodoWrite"},
		// Bash, its background output (GetTask, TaskOutput), TaskStop (also
		// KillShell, KillBash) and Monitor, which watches a shell command.
		DynaShell: true,
		Shell:     []string{"Bash", "GetTask", "TaskOutput", "TaskStop", "Monitor"},
	},
	// Pi's base package has no native memory, messaging, delegation, or cron
	// tools. Do not disable its extension system to address absent features.
	"pi": {
		// bash is Pi's only shell tool (shipped docs/cli.md). --exclude-tools
		// is its denylist and -xt is the same switch; both spellings merge
		// with a profile's own entries into one value.
		DenyFlags: []string{"--exclude-tools", "-xt"},
		DynaShell: true,
		Shell:     []string{"bash"},
	},
	"kimi-code": {
		// Kimi Code 2.3.1 documents these switches and tool names. ACP has no
		// per-session tool denylist, so the generation merges tools.disabled
		// into the private config without replacing providers or models.
		Env: map[string]string{
			"KIMI_DISABLE_CRON":                          "1",
			"KIMI_CODE_BUILTIN_PRODUCT_SKILLS":           "0",
			"KIMI_CODE_EXPERIMENTAL_FLAG":                "0",
			"KIMI_CODE_EXPERIMENTAL_TOWER":               "0",
			"KIMI_CODE_EXPERIMENTAL_SUBAGENT_FORK":       "0",
			"KIMI_CODE_BACKGROUND_KEEP_ALIVE_ON_EXIT":    "0",
			"KIMI_CODE_BACKGROUND_PRINT_BACKGROUND_MODE": "exit",
			"KIMI_CODE_TRUST_WORKSPACE":                  "0",
		},
		Config: map[string]any{
			"default_plan_mode":       false,
			"default_permission_mode": "auto",
			"telemetry":               false,
			"builtin_product_skills":  false,
			"tools.disabled": []string{
				"Agent", "AgentSwarm", "AskUserQuestion", "UpdateGoal", "TodoList",
				"EnterPlanMode", "ExitPlanMode", "WebSearch", "FetchURL",
				"CronCreate", "CronList", "CronDelete",
				"TowerInit", "TowerPlan", "TowerSpawn", "TowerMerge", "TowerRebase",
				"TowerTeardown", "TowerComplete", "TowerSend", "TowerInbox",
				"TowerFinding", "TowerReview", "TowerMission", "TowerStatus",
			}},
		DynaShell: true,
		Shell:     []string{"Bash", "TaskList", "TaskOutput", "TaskStop", "WaitFor"},
	},
}

func nativeFeaturePreferences(h string, preferences map[string]any) map[string]any {
	policy := nativeFeaturePolicies[canonicalRuntimeID(h)]
	out := make(map[string]any, len(preferences)+len(policy.Config))
	for key, value := range preferences {
		out[key] = value
	}
	for key, value := range policy.Config {
		out[key] = value
	}
	if h == "claude-code" {
		out["permissions.deny"] = policy.DenyTools
	}
	return out
}

// NativeFeatureDenyTools is the same native tool policy used by CLI and ACP launches.
func NativeFeatureDenyTools(h string, dynaShell bool) []string {
	policy := nativeFeaturePolicies[canonicalRuntimeID(h)]
	tools := slices.Clone(policy.DenyTools)
	if dynaShell {
		tools = append(tools, policy.Shell...)
	}
	return tools
}

// NativeFeatureArgs enforces the same controls for fresh and resumed commands.
// Repeated application preserves user denials and does not duplicate controls.
// dynaShell also removes the native shell tools, for a Dyna session that uses
// Dyna's MCP shell. Generations are shared by every launch, so they never do.
func NativeFeatureArgs(h string, args []string, dynaShell bool) []string {
	h = canonicalRuntimeID(h)
	policy := nativeFeaturePolicies[h]
	if dynaShell && len(policy.DenyFlags) != 0 {
		policy.DenyTools = NativeFeatureDenyTools(h, true)
	}
	out, denied := nativeFeatureDenyArgs(policy, args)
	// Normalize no-value forced flags before merging denials. Otherwise an
	// existing subset changes order on the second application.
	out = slices.DeleteFunc(out, func(arg string) bool { return slices.Contains(policy.Args, arg) })
	out = append(out, policy.Args...)
	for _, tool := range policy.DenyTools {
		if !slices.Contains(denied, tool) {
			denied = append(denied, tool)
		}
	}
	// One merged value per denylist switch keeps a profile's own entries even
	// when the policy adds no tools of its own.
	if len(denied) != 0 && len(policy.DenyFlags) != 0 {
		out = append(out, policy.DenyFlags[0], strings.Join(denied, ","))
	}
	return out
}

// DynaShellEnvNames lists the variable names of a launch environment. Dyna's
// shell server runs its jobs with the environment its own process gets, so it
// must see the same variables as the native shell. The names are known only at
// launch (session, scratch and GitHub variables), so a generation cannot list
// them. Names that are not shell identifiers cannot be referenced and are left
// out.
func DynaShellEnvNames(launchEnv []string) []string {
	var names []string
	for _, pair := range launchEnv {
		name, _, _ := strings.Cut(pair, "=")
		if name != "" && !slices.Contains(names, name) && !strings.ContainsFunc(name, func(r rune) bool {
			return r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
		}) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func nativeFeatureDenyArgs(policy nativeFeaturePolicy, args []string) ([]string, []string) {
	out := make([]string, 0, len(args))
	var denied []string
	for i := 0; i < len(args); i++ {
		option, value, equal := strings.Cut(args[i], "=")
		if !slices.Contains(policy.DenyFlags, option) {
			out = append(out, args[i])
			continue
		}
		values := []string{}
		if equal {
			values = append(values, value)
		} else {
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				values = append(values, args[i])
			}
		}
		for _, value := range values {
			for _, tool := range strings.Split(value, ",") {
				if tool = strings.TrimSpace(tool); tool != "" && !slices.Contains(denied, tool) {
					denied = append(denied, tool)
				}
			}
		}
	}
	return out, denied
}

func applyNativeFeaturePolicy(h string, g *harnessmgr.ConfigGeneration) {
	policy := nativeFeaturePolicies[canonicalRuntimeID(h)]
	if g.Env == nil {
		g.Env = map[string]string{}
	}
	applyNativeFeatureEnvironment(h, g.Env)
	g.Args = NativeFeatureArgs(h, g.Args, false)
	for _, reason := range policy.NoSwitch {
		if !slices.Contains(g.PartialReasons, reason) {
			g.PartialReasons = append(g.PartialReasons, reason)
		}
	}
}

const nativeFeatureFile = "native-features.json"

type nativeFeatureSnapshot struct {
	Version int
	Harness string
	Policy  nativeFeaturePolicy
}

func nativeFeaturePolicyBytes(h string) []byte {
	raw, _ := json.MarshalIndent(nativeFeatureSnapshot{generationInputsVersion, h, nativeFeaturePolicies[canonicalRuntimeID(h)]}, "", "  ")
	return append(raw, '\n')
}

func currentNativeFeaturePolicy(dir, h string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, nativeFeatureFile))
	return err == nil && bytes.Equal(raw, nativeFeaturePolicyBytes(h))
}

func applyNativeFeatureEnvironment(h string, env map[string]string) {
	for key, value := range nativeFeaturePolicies[canonicalRuntimeID(h)].Env {
		env[key] = value
	}
}
