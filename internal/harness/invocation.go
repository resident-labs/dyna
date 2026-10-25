package harness

import (
	"fmt"
	"strings"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// buildInvocation returns the initial worker command plus an optional exact-
// session continuation. Harnesses without a safe session contract remain
// single-shot rather than risking a duplicate fresh invocation. Dyna applies
// its shared native feature policy to every invocation.
func buildInvocation(p profile.Profile, prompt string) (inv invocation, err error) {
	return buildInvocationInDir(p, prompt, "")
}

func buildInvocationWithExecution(p profile.Profile, prompt string, exec ExecutionOptions) (inv invocation, err error) {
	return buildInvocationInDirExec(p, prompt, "", exec)
}

func buildInvocationInDir(p profile.Profile, prompt, cwd string) (inv invocation, err error) {
	return buildInvocationInDirExec(p, prompt, cwd, ExecutionOptions{})
}

func buildInvocationInDirExec(p profile.Profile, prompt, cwd string, exec ExecutionOptions) (inv invocation, err error) {
	definition, ok := profile.HarnessDefinitionFor(p.Harness)
	if !ok {
		return invocation{}, fmt.Errorf("unknown harness %q", p.Harness)
	}
	// Compaction args reach every dialect, first turn and resume, as extraArgs.
	// runOnce adds the environment half.
	compactArgs, _, err := profile.CompactNative(p)
	if err != nil {
		return invocation{}, err
	}
	if p.Harness == profile.HarnessClaudeCode {
		repositoryInstructions, err := claudeRepositoryInstructions(cwd)
		if err != nil {
			return invocation{}, err
		}
		if repositoryInstructions != "" {
			p.RuntimeInstructions += "\n\n" + repositoryInstructions
		}
	}
	instructionArgs, err := instructionsNative(p)
	if err != nil {
		return invocation{}, err
	}
	// Check user permission choices before merging generated policy arguments.
	permissionArgs := p.RuntimePermissionArgs
	if permissionArgs == nil {
		permissionArgs = p.ExtraArgs
	}
	explicitClaudePermissions := claudeHasExplicitPermissions(permissionArgs)
	p.ExtraArgs = append([]string{}, p.ExtraArgs...)
	p.ExtraArgs = append(p.ExtraArgs, compactArgs...)
	p.ExtraArgs = append(p.ExtraArgs, instructionArgs...)
	if exec.DisableNativeSubagents && p.Harness == profile.HarnessCodex && codexExplicitlyEnablesMultiAgent(p.ExtraArgs) {
		return invocation{}, fmt.Errorf("codex profile cannot enable multi_agent for this invocation: native subagents are disabled for this invocation")
	}
	p.ExtraArgs = harnessruntime.NativeFeatureArgs(p.Harness, p.ExtraArgs, exec.DynaShell)
	switch definition.Invocation {
	case "claude-print":
		if exec.DisableNativeSubagents && (hasOptionValue(p.ExtraArgs, "--allowedTools", "Agent") || hasOptionValue(p.ExtraArgs, "--allowed-tools", "Agent") || hasOptionValue(p.ExtraArgs, "--allowedTools", "Workflow") || hasOptionValue(p.ExtraArgs, "--allowed-tools", "Workflow")) {
			return invocation{}, fmt.Errorf("claude profile cannot allow the Agent or Workflow tool for this invocation: native subagents are disabled for this invocation")
		}
		claudeExtraArgs := p.ExtraArgs
		claudeExtraArgs, err = claudeArgsWithoutAttribution(claudeExtraArgs, cwd)
		if err != nil {
			return invocation{}, err
		}
		streamJSON := p.EffectiveDialect() == workeroutput.DialectClaudeStreamJSON
		var streamArgs []string
		if streamJSON {
			if hasAnyOption(p.ExtraArgs, "--output-format", "--input-format", "--include-partial-messages", "--replay-user-messages") {
				return invocation{}, fmt.Errorf("claude profile extraArgs may not set --output-format/--input-format/--include-partial-messages/--replay-user-messages with the claude-stream-json dialect; dyna reserves them to deliver steering on stdin, observe tool boundaries, and capture the final response")
			}
			streamArgs = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages"}
			// Headless Claude omits thinking text unless a display is set.
			if !hasAnyOption(p.ExtraArgs, "--thinking-display") {
				streamArgs = append(streamArgs, "--thinking-display", "summarized")
			}
		}
		argv := append([]string{definition.Binary, "-p"}, streamArgs...)
		// Workers run headless and must act autonomously; permission prompts
		// would hang forever. SafeMode or explicit permission flags opt out.
		if !p.SafeMode && !explicitClaudePermissions {
			argv = append(argv, "--dangerously-skip-permissions")
		}
		if p.Model != "" {
			argv = append(argv, "--model", p.Model)
		}
		resumable := !hasAnyOption(p.ExtraArgs, "--session-id", "--resume", "-r", "--continue", "-c", "--fork-session", "--no-session-persistence", "--max-budget-usd")
		var sessionID string
		if resumable {
			sessionID, err = newSessionID()
			if err != nil {
				return invocation{}, fmt.Errorf("create claude session id: %w", err)
			}
			argv = append(argv, "--session-id", sessionID)
		}
		argv = append(argv, claudeExtraArgs...)
		var parseClaude func(string) string
		if streamJSON {
			parseClaude = claudeStreamOutput
		}
		inv.initial = commandSpec{argv: argv, stdinPrompt: true, prompt: prompt, parseOutput: parseClaude, streamInput: streamJSON}
		if resumable {
			inv.sessionID = func(string) string { return sessionID }
			inv.resume = func(id, nudge string) commandSpec {
				args := append([]string{definition.Binary, "-p"}, streamArgs...)
				args = append(args, definition.Resume, id)
				if !p.SafeMode && !explicitClaudePermissions {
					args = append(args, "--dangerously-skip-permissions")
				}
				if p.Model != "" {
					args = append(args, "--model", p.Model)
				}
				args = append(args, claudeExtraArgs...)
				return commandSpec{argv: args, stdinPrompt: true, prompt: nudge, parseOutput: parseClaude, streamInput: streamJSON}
			}
		}
		return inv, nil

	case "codex-app-server":
		return codexInvocation(p, prompt, exec)

	case "opencode-run":
		return openCodeInvocation(p, prompt, exec)

	case "muse-exec":
		return museInvocation(p, prompt, exec)

	case "pi-print":
		return piInvocation(p, prompt, exec)

	case "command":
		var argv []string
		hasPrompt := false
		for _, a := range p.Command {
			out := strings.ReplaceAll(a, "{{model}}", p.Model)
			if strings.Contains(out, "{{prompt}}") {
				out = strings.ReplaceAll(out, "{{prompt}}", prompt)
				hasPrompt = true
			}
			argv = append(argv, out)
		}
		inv.initial = commandSpec{argv: argv, stdinPrompt: !hasPrompt, prompt: prompt}
		return inv, nil
	}
	return invocation{}, fmt.Errorf("unknown harness %q", p.Harness)
}

// CodexCodeModeOverride is the per-invocation config dyna passes to every
// Codex it launches (codex-cli 0.160): code mode off, and a 10-minute exec
// yield. Models whose catalog entry sets tool_mode=code_mode_only (all of
// gpt-5.6/gpt-6 today) keep code mode regardless of the flag; for them the
// long yield is what matters: a tool call inside an exec cell is not handed
// back to the model as "still running" every 30 s (the CLI default), which
// otherwise costs one model call per yield while the tool runs.
const CodexCodeModeOverride = "features.code_mode={enabled=false,default_exec_yield_time_ms=600000}"

// codexCodeModeArgs returns the code-mode override unless the profile already
// configures code mode itself.
func codexCodeModeArgs(extra []string) []string {
	if CodexMentionsCodeMode(extra) {
		return nil
	}
	return []string{"-c", CodexCodeModeOverride}
}

// CodexMentionsCodeMode reports whether args already enable/disable code mode.
func CodexMentionsCodeMode(args []string) bool {
	for i, a := range args {
		if strings.Contains(a, "code_mode") && i > 0 {
			switch args[i-1] {
			case "--enable", "--disable", "-c", "--config":
				return true
			}
		}
		if strings.HasPrefix(a, "--enable=") || strings.HasPrefix(a, "--disable=") || strings.HasPrefix(a, "--config=") {
			if strings.Contains(a, "code_mode") {
				return true
			}
		}
	}
	return false
}
