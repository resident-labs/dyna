package harness

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/profile"
)

type codexTurnPolicy struct {
	approval  string
	sandbox   string
	ephemeral bool
	cwd       string
}

func codexInvocation(p profile.Profile, prompt string, execution ExecutionOptions) (inv invocation, err error) {
	if hasCodexOutputArg(p.ExtraArgs) {
		return inv, fmt.Errorf("codex profile extraArgs may not set -o/--output-last-message; dyna captures the final response from app-server")
	}
	// app-server parses some global TUI flags but does not apply them. Never
	// silently discard a provider or permission profile.
	if hasAnyOption(p.ExtraArgs, "--profile", "-p", "--oss", "--local-provider") {
		return inv, fmt.Errorf("codex app-server does not support --profile/--oss/--local-provider extraArgs; use explicit -c configuration")
	}
	key, err := sessionKey(p, execution)
	if err != nil {
		return inv, err
	}
	policy := codexTurnPolicy{ephemeral: hasAnyOption(p.ExtraArgs, "--ephemeral")}
	if !codexHasExplicitPermissions(p.ExtraArgs) {
		policy.approval, policy.sandbox = "never", "danger-full-access"
		if p.SafeMode {
			policy.approval, policy.sandbox = "on-request", "read-only"
		}
	}
	args := []string{"codex"}
	for i := 0; i < len(p.ExtraArgs); i++ {
		arg := p.ExtraArgs[i]
		// These exec-only switches become protocol behavior.
		if arg == "--json" || arg == "--skip-git-repo-check" || arg == "--ephemeral" {
			continue
		}
		args = append(args, arg)
	}
	// Global CLI options precede the subcommand. Hook trust is a global
	// option and app-server rejects it after the subcommand.
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	if policy.sandbox == "danger-full-access" {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, codexAttributionArgs(p.ExtraArgs)...)
	args = append(args, codexCodeModeArgs(p.ExtraArgs)...)
	args = append(args, "app-server")
	for i, arg := range p.ExtraArgs {
		key, value, inline := strings.Cut(arg, "=")
		if !inline && i+1 < len(p.ExtraArgs) {
			value = p.ExtraArgs[i+1]
		}
		switch key {
		case "--sandbox", "-s":
			policy.sandbox = value
		case "--ask-for-approval", "-a":
			policy.approval = value
		case "--cd", "-C":
			policy.cwd = value
		case "--dangerously-bypass-approvals-and-sandbox":
			policy.approval, policy.sandbox = "never", "danger-full-access"
		}
	}
	inv.initial = commandSpec{argv: args, prompt: prompt, parseOutput: codexAppServerOutput, codex: true, codexKey: key, codexPolicy: policy}
	if !policy.ephemeral {
		inv.sessionID = codexSessionID
		inv.resume = func(id, prompt string) commandSpec {
			return commandSpec{argv: args, prompt: prompt, parseOutput: codexAppServerOutput, codex: true, codexKey: key, codexThread: id, codexPolicy: policy}
		}
	}
	return inv, nil
}

func (p codexTurnPolicy) params(thread bool) map[string]any {
	out := map[string]any{}
	if p.approval != "" {
		out["approvalPolicy"] = p.approval
	}
	if p.sandbox != "" {
		if thread {
			out["sandbox"] = p.sandbox
		} else {
			kind := map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}[p.sandbox]
			if kind != "" {
				out["sandboxPolicy"] = map[string]any{"type": kind}
			}
		}
	}
	return out
}

// Keep the last final answer. Commentary stays visible in the transcript but
// cannot satisfy a required final response.
func codexAppServerOutput(stdout string) string {
	var answer string
	for _, line := range strings.Split(stdout, "\n") {
		var record struct {
			Type string `json:"type"`
			Item struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Phase string `json:"phase"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &record) == nil && record.Type == "item.completed" && record.Item.Type == "agent_message" && (record.Item.Phase == "" || record.Item.Phase == "final_answer") {
			answer = record.Item.Text
		}
	}
	return answer
}
