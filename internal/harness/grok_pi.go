package harness

import (
	"fmt"
	"strings"

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
			// builtin:codemode is how Pi reaches catalog MCP tools that keep its
			// default exposure; Dyna's own shell is exposed directly.
			args = append(args, "--no-extensions", "-e", "builtin:mcp", "-e", "builtin:codemode")
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
	inv.resume = func(sessionID, prompt string) commandSpec {
		// --session opens an existing session across projects or exits with
		// "session not found". --session-id would silently create a fresh
		// conversation if the original disappeared.
		return commandSpec{argv: append(base(), "--session", sessionID), prompt: prompt, parseOutput: workeroutput.PiOutput, rpc: true}
	}
	inv.fork = func(sourcePath, prompt string) commandSpec {
		// --fork copies the source session into this turn's directory and
		// starts a new session there without asking. --session-id pins its id,
		// so the next turn resumes the fork instead of forking again.
		return commandSpec{argv: append(base(), "--fork", sourcePath, "--session-id", id), prompt: prompt, parseOutput: workeroutput.PiOutput, rpc: true}
	}
	// Compaction runs on the session's rpc process, not a second pi.
	inv.compact = func(sessionID string) commandSpec {
		return commandSpec{argv: append(base(), "--session", sessionID), rpc: true}
	}
	return inv, nil
}

// piMissingSessionError recognizes the stable headless Pi startup error
// for the exact --session id in this invocation. Other stderr keeps the
// ordinary failure and recovery path.
func piMissingSessionError(p profile.Profile, argv []string, a attempt) error {
	if a.runErr == nil || a.contextDone || p.Harness != profile.HarnessPi {
		return nil
	}
	id := piOptionValue(argv, "--session")
	if id == "" {
		return nil
	}
	want := "session not found: " + id
	for _, line := range strings.Split(a.stderr, "\n") {
		if strings.TrimSpace(line) == want {
			return fmt.Errorf("worker %s cannot resume harness session %s: pi session %s has no session file", p.Name, id, id)
		}
	}
	return nil
}
