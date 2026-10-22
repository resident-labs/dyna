package hooks

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/processguard"
)

func validateHookMetadata(env []string) error {
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "DYNA_HOOK_") {
			continue
		}
		for _, r := range value {
			if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || strings.ContainsRune("|&;()<>$`\\\"'{}[]*?!~", r) {
				return fmt.Errorf("hook metadata %s contains shell metacharacters or control characters; command refused", key)
			}
		}
	}
	return nil
}

func (s Service) bindHook(name string, requested bool) func(context.Context, processguard.Reference) error {
	return func(ctx context.Context, ref processguard.Reference) error {
		actor := callerid.Identity{Kind: callerid.Owner}
		if requested {
			a, ok := authority.ContextActor(ctx)
			if !ok {
				return fmt.Errorf("an explicit hook run requires an authenticated caller")
			}
			switch a.Role {
			case authority.Owner:
			case authority.Root, authority.Session, authority.Steward:
				actor = callerid.Identity{Kind: callerid.Session, Session: a.Session}
			case authority.Worker:
				actor = callerid.Identity{Kind: callerid.Worker, Run: a.Run, Agent: a.Agent}
			default:
				return fmt.Errorf("an explicit hook run cannot act for role %q", a.Role)
			}
		}
		pid, birth := ref.Leader()
		start, err := strconv.ParseUint(birth, 10, 64)
		if err != nil {
			return fmt.Errorf("read hook anchor start time: %w", err)
		}
		return callerid.RecordHook(ctx, callerid.Process{PID: pid, Start: start}, s.dataDir(), name, actor)
	}
}
