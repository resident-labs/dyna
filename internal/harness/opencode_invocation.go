package harness

import (
	"dyna-agent/internal/profile"
	"fmt"
	"strings"
)

func openCodeInvocation(p profile.Profile, prompt string, execution ExecutionOptions) (inv invocation, err error) {
	if p.Model != "" && !strings.Contains(p.Model, "/") {
		return inv, fmt.Errorf("OpenCode model must use provider/model format")
	}
	key, err := sessionKey(p, execution)
	if err != nil {
		return inv, err
	}
	// CLI presentation flags are irrelevant to the native event stream.
	for i := 0; i < len(p.ExtraArgs); i++ {
		a := p.ExtraArgs[i]
		switch a {
		case "--thinking", "--standalone":
		case "--format", "--variant":
			i++
		default:
			if len(a) > 9 && a[:9] == "--format=" {
				continue
			}
			if len(a) > 10 && a[:10] == "--variant=" {
				continue
			}
			return inv, fmt.Errorf("OpenCode serve does not support extra argument %s", a)
		}
	}
	spec := commandSpec{argv: []string{"opencode", "serve"}, prompt: prompt, parseOutput: openCodeOutput, openCode: true, openCodeKey: key}
	inv.initial = spec
	inv.sessionID = openCodeSessionID
	inv.resume = func(id, text string) commandSpec {
		next := spec
		next.openCodeSession = id
		next.prompt = text
		return next
	}
	inv.compact = func(id string) commandSpec {
		wait := commandSpec{openCode: true, openCodeKey: key, openCodeSession: id, argv: spec.argv, openCodeOperation: "wait"}
		after := wait
		compact := wait
		compact.openCodeOperation = "compact"
		compact.after = &after
		wait.after = &compact
		return wait
	}
	return inv, nil
}
