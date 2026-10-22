package subagent

import (
	"fmt"

	"dyna-agent/internal/profile"
)

// checkProfileUse refuses a profile whose use excludes this kind of session.
func checkProfileUse(p profile.Profile, service bool) error {
	if service && !p.ForServices() {
		return fmt.Errorf("profile %q is limited to subagent sessions; Dyna services cannot use it. Pick another or run `dyna profiles set %s --use all`", p.Name, p.Name)
	}
	if !service && !p.ForSubagents() {
		return fmt.Errorf("profile %q is limited to Dyna services; pick another from `dyna profiles list`", p.Name)
	}
	return nil
}
