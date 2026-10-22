package harness

import (
	"context"
	"dyna-agent/internal/computers"
)

// A desktop failure must not prevent a session from running non-GUI work.
func computerEnvironment(ctx context.Context, base []string) []string {
	env, err := computers.Inject(ctx, base)
	if err != nil {
		computers.RecordFailure(environmentValue(base, "DYNA_SESSION"), err)
		return base
	}
	return env
}
func environmentValue(env []string, key string) string {
	prefix := key + "="
	for _, line := range env {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return line[len(prefix):]
		}
	}
	return ""
}
