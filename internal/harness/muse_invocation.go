package harness

import (
	"fmt"
	"strings"

	"dyna-agent/internal/profile"
)

func museInvocation(p profile.Profile, prompt string, execution ExecutionOptions) (inv invocation, err error) {
	if hasAnyOption(p.ExtraArgs, "--json", "--session-id", "--model", "--prompt-file", "--output-schema") {
		return inv, fmt.Errorf("muse profile extraArgs may not set --json/--session-id/--model/--prompt-file/--output-schema; dyna reserves them for MSP")
	}
	key, err := sessionKey(p, execution)
	if err != nil {
		return inv, err
	}
	id, err := museCommandID()
	if err != nil {
		return inv, err
	}
	permission, approval := ":unrestricted", "allowAll"
	if p.SafeMode || museHasExplicitPermissions(p.ExtraArgs) {
		permission, approval = ":ask-me", "promptUnmatched"
	}
	if hasAnyOption(p.ExtraArgs, "--yolo", "--disable-approval") {
		permission, approval = ":unrestricted", "allowAll"
	}
	if v, ok := argumentValue(p.ExtraArgs, "--permission-profile"); ok {
		permission = v
	}
	if v, ok := argumentValue(p.ExtraArgs, "--approval-mode"); ok {
		approval = v
	}
	args := []string{"muse", "serve", "--trust-workspace"}
	if permission == ":unrestricted" {
		args = append(args, "--disable-sandbox")
	}
	// Serve owns sandbox posture; permissions and reasoning travel over MSP.
	for i := 0; i < len(p.ExtraArgs); i++ {
		arg := p.ExtraArgs[i]
		name, _, inline := strings.Cut(arg, "=")
		switch name {
		case "--yolo", "--disable-approval":
			continue
		case "--permission-profile", "--approval-mode", "--reasoning-effort":
			if !inline {
				i++
			}
			continue
		case "--no-foreign-personal-context":
			args = append(args, "-c", "context.foreign_personal_rules=false", "-c", "context.foreign_personal_skills=false")
			continue
		}
		args = append(args, arg)
	}
	if p.Model != "" {
		args = append(args, "--model", p.Model)
	}
	effort, _ := argumentValue(p.ExtraArgs, "--reasoning-effort")
	spec := commandSpec{museReasoning: effort, argv: args, prompt: prompt, parseOutput: museOutput, muse: true, museKey: key, museSession: id, musePermission: permission, museApproval: approval}
	inv.initial = spec
	inv.sessionID = museSessionID
	inv.resume = func(id, prompt string) commandSpec {
		next := spec
		next.museSession = id
		next.museResume = true
		next.prompt = prompt
		return next
	}
	return inv, nil
}
