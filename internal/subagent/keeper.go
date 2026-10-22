package subagent

import (
	"fmt"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
)

func (m *Manager) turnDeadline(role string, deadline time.Time) time.Time {
	if role != RoleKeeper || !deadline.IsZero() {
		return deadline
	}
	timeout := m.settings().GoalKeeperTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return time.Now().Add(timeout)
}

// KeeperProfile rejects unsupported harnesses and constrains keeper permissions.
// Codex config overrides also work on exec resume, unlike --sandbox.
func KeeperProfile(p profile.Profile) (profile.Profile, error) {
	p.SafeMode = true
	switch p.Harness {
	case profile.HarnessCodex:
		// This replaces the generation's arguments, so the keeper has no
		// "dyna" MCP server. Its sandbox does not cover MCP tools. Do not add
		// -c mcp_servers.dyna.enabled=false: without the server's definition,
		// Codex fails to load its config.
		p.ExtraArgs = []string{"-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`}
	case profile.HarnessClaudeCode:
		// Only the named file tools and command families are available. Ignore
		// inherited settings and MCP servers, which could add broader access.
		// Unapproved commands fail instead of waiting for an interactive prompt.
		p.ExtraArgs = []string{
			"--permission-mode", "dontAsk", "--permission-prompts", "none",
			"--setting-sources", "", "--strict-mcp-config",
			"--tools", "Read,Glob,Grep,Bash",
			"--allowedTools", "Read,Glob,Grep,Bash(dyna room log *),Bash(dyna goal show *),Bash(dyna goal list*),Bash(dyna goal journal*),Bash(dyna room read *),Bash(dyna git pr merge-status*),Bash(dyna subagent show *),Bash(dyna git pr show *),Bash(gh pr view *),Bash(gh pr checks *),Bash(git log *),Bash(git diff *)",
			"--disallowedTools", "Edit,Write,NotebookEdit,Bash(git *--output*),Bash(git *--ext-diff*),Bash(git *--textconv*)",
		}
	case profile.HarnessMock:
	default:
		return profile.Profile{}, fmt.Errorf("keeper profile %q uses unsupported harness %q; use claude-code or codex", p.Name, p.Harness)
	}
	return p, nil
}

func sessionExecution(role string) harness.ExecutionOptions {
	// The keeper keeps the native shell: its read-only permissions are
	// patterns on that tool.
	exec := harness.ExecutionOptions{
		DisableNativeSubagents: true,
		DynaShell:              role != RoleKeeper,
		KillOnCancel:           role == RoleKeeper,
		SkipComputer:           role == RoleKeeper,
	}
	if role == RoleKeeper {
		exec.ConstrainProfile = KeeperProfile
	}
	return exec
}
