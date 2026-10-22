package harness

import (
	"fmt"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func piInvocation(p profile.Profile, prompt string, exec ExecutionOptions) (inv invocation, err error) {
	if hasAnyOption(p.ExtraArgs, "--mode", "--print", "-p", "--model", "--session", "--session-id", "--no-session", "--resume", "-r", "--continue", "-c", "--fork", "--export") {
		return inv, fmt.Errorf("pi extraArgs conflict with reserved headless output, model, prompt, or session options")
	}
	id, err := newSessionID()
	if err != nil {
		return inv, err
	}
	base := func() []string {
		// One long-lived rpc process per session runs every turn: prompts,
		// steering, aborts and compaction travel as JSONL commands.
		args := []string{"pi", "--mode", "rpc"}
		// Pi has no permission prompt for built-in tools. Trust controls project
		// extensions and configuration, so SafeMode leaves that trust unchanged.
		if !p.SafeMode && !hasAnyOption(p.ExtraArgs, "--approve", "-a", "--no-approve", "-na") {
			args = append(args, "--approve")
		}
		// Pi extensions can introduce delegation tools; on sessions Dyna does
		// its own delegation, so disable extension discovery and reject
		// explicit extension loading there. MCP support is the builtin:mcp
		// extension (shipped docs/cli.md), so load that one alone: without it
		// a session loses every MCP tool, Dyna's shell included.
		if exec.DisableNativeSubagents {
			args = append(args, "--no-extensions", "-e", "builtin:mcp")
		}
		if p.Model != "" {
			args = append(args, "--model", p.Model)
		}
		return append(args, p.ExtraArgs...)
	}
	if exec.DisableNativeSubagents && hasAnyOption(p.ExtraArgs, "--extension", "-e") {
		return inv, fmt.Errorf("pi cannot load extensions when native subagents are disabled")
	}
	inv.initial = commandSpec{argv: append(base(), "--session-id", id), prompt: prompt, parseOutput: workeroutput.PiOutput, rpc: true}
	// The id is preassigned on the command line; no stdout record is needed.
	inv.sessionID = func(string) string { return id }
	inv.resume = func(id, prompt string) commandSpec {
		// --session opens an existing session or fails. --session-id would
		// silently create a fresh conversation if the original disappeared.
		return commandSpec{argv: append(base(), "--session", id), prompt: prompt, parseOutput: workeroutput.PiOutput, rpc: true}
	}
	// Compaction runs on the session's rpc process, not a second pi.
	inv.compact = func(id string) commandSpec {
		return commandSpec{argv: append(base(), "--session", id), rpc: true}
	}
	return inv, nil
}
