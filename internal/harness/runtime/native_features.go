package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	harnessmgr "dyna-agent/internal/harness/config"

	"github.com/pelletier/go-toml/v2"
)

// Dyna owns memory, delegation, messaging, and session wakeups. Keep every
// verified native control here, including explicit gaps. Evidence applies to
// Claude 2.1.302, Codex 0.163.0, Grok 1.0.62, Pi 1.3.0, OpenCode 2.0.32,
// Muse 1.4.10-R6427.1, and Antigravity ACP 1.3.0 (2026-10-21).
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
	"codex": {
		// learn.chatgpt.com/docs/config-file/config-reference and features list.
		Config: map[string]any{
			// Feature flags alone defer to the model catalog's agent version.
			"agents.enabled": false,
			"tools.experimental_request_user_input.enabled": false,
			"tools.update_plan.enabled":                     false,
			"features.apps":                                 false,
			"features.plugins":                              false,
			"features.memories":                             false,
			"features.external_agent_memory_import":         false,
			"features.multi_agent":                          false,
			"features.multi_agent_v2":                       false,
			"features.agent_message_board":                  false,
			"features.goals":                                false,
			"features.in_app_local_automation":              false,
			"features.remote_plugin":                        false,
		},
		// Shell lists the features to --disable: exec_command and write_stdin
		// (unified_exec) and the shell tool. Every catalog model uses unified_exec,
		// so no local_shell tool stays offered (`codex debug models`).
		DynaShell: true,
		Shell:     []string{"shell_tool", "unified_exec"},
	},
	"grok": {
		// docs.x.ai/build/settings/reference, installed help and embedded docs.
		// Grok runs over ACP (`grok agent stdio`): the print-mode flags
		// (--no-memory, --disallowed-tools) do not exist there, so every control
		// is an environment variable or a config.toml key in the private home.
		Env: map[string]string{"GROK_MEMORY": "0", "GROK_SUBAGENTS": "0", "GROK_WORKFLOWS": "0", "GROK_GOAL": "0", "GROK_ASK_USER_QUESTION": "0"},
		// Tools without a feature switch are denied by a config.toml
		// PreToolUse hook (the hooks-in-config format grok 1.0.62 documents).
		// send_subagent_message is Grok's messaging tool (#1863).
		Config:    map[string]any{"hooks.PreToolUse": []any{grokDenyHook("scheduler_create|scheduler_list|scheduler_delete|monitor|enter_plan_mode|exit_plan_mode|todo_write|send_subagent_message")}},
		DynaShell: true,
		// run_terminal_command is the native shell; the other three name its
		// background tasks. The generation adds them to the deny hook when
		// Dyna's MCP shell server is configured.
		Shell:    grokShellTools,
		NoSwitch: []string{"No switch verified for Grok restoring existing scheduled tasks; the deny hook prevents new schedules."},
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
	"opencode": {
		// opencode.ai/v2/docs/policies: hard denies run after agent permissions.
		// OpenCode has no per-launch switch, so DynaShell turns the native shell
		// off in the generation that every launch of the account shares. A
		// permission deny drops the tool from the model's list; a tool.use
		// policy would only block its calls. The shared service gives the MCP
		// server no session of its own, so "dyna mcp shell" maps each call to
		// its Dyna session by OpenCode's session ID. Shell stays empty:
		// nothing is removed per launch.
		DynaShell: true,
		Config: map[string]any{"permission.shell": "deny", "policies": []any{
			map[string]any{"action": "tool.use", "resource": "subagent:*", "effect": "deny"},
			map[string]any{"action": "tool.use", "resource": "question:*", "effect": "deny"},
			// Project discovery reaches ancestor homes. Only generation-managed
			// skills may load; names alone cannot safely identify built-ins.
			map[string]any{"action": "integration.use", "resource": "skill:*", "effect": "deny"},
		}},
	},
	"muse": {
		Args: []string{"--disable-request-user-input", "--no-foreign-personal-context"},
		// Muse has no tool denylist, so its Shell entries are forced flags:
		// --disable-shell is the one switch that removes both native shell
		// tools, shell and bash_input.
		DynaShell: true,
		Shell:     []string{"--disable-shell"},
		// Native config validation and plugins reject resolve these exact IDs.
		// An echo-provider startup probe confirms enabled=false removes the
		// observer roster without a version-specific trusted_definition_hash.
		Config: map[string]any{
			"local_session_messaging.enabled": false,
			"run.subagent_delegation_mode":    "off",
			"run.workflow_trigger_mode":       "off",
			"agents.execution_capacity":       1,
			"runtime_capabilities": map[string]any{
				"plugin:memory-reminder:reminder:memory-reminder": map[string]any{"enabled": false},
				"plugin:skill-reminder:reminder:skill-reminder":   map[string]any{"enabled": false},
				"plugin:todo-reminder:reminder:todo-reminder":     map[string]any{"enabled": false},
				"plugin:goal-reminder:reminder:goal-reminder":     map[string]any{"enabled": false},
				"plugin:verify-reminder:reminder:verify-reminder": map[string]any{"enabled": false},
				"plugin:scope-reminder:reminder:scope-reminder":   map[string]any{"enabled": false},
			},
		},
		// Native memory tools (read_memory, add_memory, edit_memory) are denied
		// by the PreToolUse hook in steerhook.go. Muse before 1.4.7 cannot load
		// that hook, so its memory tools stay enabled there.
		NoSwitch: []string{
			"No switch verified for Muse persistent memory loading; disabling observers does not disable the startup memory index.",
			"Muse before 1.4.7 cannot load Dyna's managed hooks, so its native memory tools stay enabled.",
		},
	},
	"kimi-code": {
		// Kimi Code 2.3.1 documents these switches and tool names. ACP has no
		// per-session tool denylist, so the generation merges tools.disabled
		// into the private config without replacing providers or models.
		Env: map[string]string{
			"KIMI_DISABLE_CRON":                          "1",
			"KIMI_CODE_EXPERIMENTAL_FLAG":                "0",
			"KIMI_CODE_EXPERIMENTAL_TOWER":               "0",
			"KIMI_CODE_EXPERIMENTAL_SUBAGENT_FORK":       "0",
			"KIMI_CODE_BACKGROUND_KEEP_ALIVE_ON_EXIT":    "0",
			"KIMI_CODE_BACKGROUND_PRINT_BACKGROUND_MODE": "exit",
			"KIMI_CODE_TRUST_WORKSPACE":                  "1",
		},
		Config: map[string]any{"tools.disabled": []string{
			"Agent", "AgentSwarm", "AskUserQuestion", "UpdateGoal", "TodoList",
			"CronCreate", "CronList", "CronDelete",
			"TowerInit", "TowerPlan", "TowerSpawn", "TowerMerge", "TowerRebase",
			"TowerTeardown", "TowerComplete", "TowerSend", "TowerInbox",
			"TowerFinding", "TowerReview", "TowerMission", "TowerStatus",
		}},
		DynaShell: true,
		Shell:     []string{"Bash", "TaskList", "TaskOutput", "TaskStop", "WaitFor"},
	},
	"antigravity": {
		// Pinned ACP tool_filter.py accepts _meta.agy.disabledTools on new/load/resume.
		// server.py removes these builtins before constructing localharness capabilities.
		DenyTools: []string{"start_subagent", "schedule"},
		DynaShell: true,
		Shell:     []string{"run_command"},
		NoSwitch: []string{
			"No switch verified for Antigravity native memory.",
			"No switch verified for Antigravity native messaging.",
			"No switch verified for Antigravity ACP /plan expansion.",
		},
	},
}

// grokShellTools are Grok's native shell tool and its background-task tools.
var grokShellTools = []string{"run_terminal_command", "get_command_or_subagent_output", "wait_commands_or_subagents", "kill_command_or_subagent"}

// grokDenyHook builds one config.toml [[hooks.PreToolUse]] entry that denies
// the tools matching the regex with a fixed command, mirroring the Antigravity
// plugin hook. A deny stands under --always-approve; only an ask would be
// auto-approved away (grok 1.0.62 hooks guide).
func grokDenyHook(matcher string) map[string]any {
	return map[string]any{
		"matcher": matcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": `printf '%s' '{"decision":"deny","reason":"Dyna owns this capability. Use your goal, the room, Dyna shell or files you own instead."}'`,
		}},
	}
}

// GrokShellDenyHook is the entry that removes Grok's native shell tools from
// a generation that configures Dyna's MCP shell server.
func GrokShellDenyHook() map[string]any {
	return grokDenyHook(strings.Join(grokShellTools, "|"))
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
	if dynaShell {
		switch {
		case len(policy.DenyFlags) != 0:
			policy.DenyTools = NativeFeatureDenyTools(h, true)
		case len(policy.Shell) != 0 && strings.HasPrefix(policy.Shell[0], "-"):
			// Muse has no tool denylist: its Shell entries are the flags
			// that remove both native shell tools, merged like the forced
			// args. Codex lists feature names instead, handled below.
			policy.Args = append(slices.Clone(policy.Args), policy.Shell...)
		}
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
	if h == "codex" {
		var disabled []string
		for key, value := range policy.Config {
			if strings.HasPrefix(key, "features.") && value == false {
				disabled = append(disabled, strings.TrimPrefix(key, "features."))
			}
		}
		if dynaShell {
			disabled = append(disabled, policy.Shell...)
		}
		slices.Sort(disabled)
		// Codex lowers --enable after -c overrides. Remove conflicting enables
		// before appending forced disables, including mixed feature lists.
		var filtered []string
		for i := 0; i < len(out); i++ {
			option, value, equal := strings.Cut(out[i], "=")
			if option != "--enable" && option != "--disable" {
				filtered = append(filtered, out[i])
				continue
			}
			if !equal && i+1 < len(out) {
				i++
				value = out[i]
			}
			for _, feature := range strings.Split(value, ",") {
				if !slices.Contains(disabled, feature) {
					filtered = append(filtered, option, feature)
				}
			}
		}
		out = filtered
		for _, feature := range disabled {
			out = append(out, "--disable", feature)
		}
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

// DynaShellEnvArgs makes Codex pass the whole launch environment to the "dyna"
// MCP server, as the native shell gets it. Codex otherwise starts a server with
// only a small default set. It must follow the generation's arguments: a later
// dotted -c override merges into its server.
func DynaShellEnvArgs(launchEnv []string) []string {
	return []string{"-c", "mcp_servers." + dynaMCPName + ".env_vars=" + strings.ReplaceAll(fmt.Sprintf("%q", DynaShellEnvNames(launchEnv)), " ", ",")}
}

// DynaShellCodexArgs forwards the launch environment only when the generated
// mcp_servers={...} table defines Dyna. Dotted server keys are not recognized.
// An env-only table has no transport and Codex rejects it.
func DynaShellCodexArgs(argv, launchEnv []string) ([]string, error) {
	configured := false
	for i := 1; i+1 < len(argv); i++ {
		if argv[i] != "-c" || !strings.HasPrefix(argv[i+1], "mcp_servers=") {
			continue
		}
		var config struct {
			Servers map[string]map[string]any `toml:"mcp_servers"`
		}
		if err := toml.Unmarshal([]byte(argv[i+1]), &config); err != nil {
			return nil, fmt.Errorf("read generated Codex MCP servers: %w", err)
		}
		_, configured = config.Servers[dynaMCPName]
	}
	if !configured {
		return argv, nil
	}
	// Insert before the final subcommand (app-server), or the prompt marker
	// of another Codex invocation. Config options are global.
	at := len(argv) - 1
	return slices.Concat(argv[:at], DynaShellEnvArgs(launchEnv), argv[at:]), nil
}

// DynaShellMuseArgs completes Muse's generated "mcpServers.dyna" -c pair with
// the whole launch environment, as the native shell gets it. Muse starts a
// server with a small default set only. Muse refuses both a repeated -c key and
// a dotted one beside its parent, so the launch replaces its own pair in place.
// Every value is a whole-value ${VAR} reference naming a variable of that same
// launch: Muse's -c shape accepts no other secret form, not even a ${VAR:-}
// default (Muse 1.4.10 probe), and every name here is set in the launch
// environment it comes from.
func DynaShellMuseArgs(argv, launchEnv []string) ([]string, error) {
	const prefix = "mcpServers." + dynaMCPName + "="
	env := map[string]string{}
	for _, name := range DynaShellEnvNames(launchEnv) {
		env[name] = "${" + name + "}"
	}
	out := slices.Clone(argv)
	for i := 1; i < len(out); i++ {
		raw, ok := strings.CutPrefix(out[i], prefix)
		if !ok || out[i-1] != "-c" {
			continue
		}
		entry := map[string]any{}
		if json.Unmarshal([]byte(raw), &entry) != nil {
			return nil, errors.New("the generated Muse MCP pair for Dyna's shell server is not JSON")
		}
		entry["env"] = env
		next, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		out[i] = prefix + string(next)
	}
	return out, nil
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
