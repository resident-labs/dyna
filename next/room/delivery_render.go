package room

import (
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// RenderDelivery renders the event bodies that may be delivered to a session.
func RenderDelivery(body contract.EventBody) (string, error) {
	switch b := body.(type) {
	case contract.SessionProfileChanged:
		return fmt.Sprintf("Your model is now %s.", b.Profile), nil
	case contract.PostCreated:
		return b.Text, nil
	case contract.SessionRestarted:
		return "Your box was replaced. Your conversation is restored. Unpushed work in the old volume is gone.", nil
	case contract.SessionStarted:
		var parts []string
		if b.Goal != "" {
			parts = append(parts, "Goal: "+b.Goal)
		}
		if b.Brief != "" {
			parts = append(parts, b.Brief)
		}
		return strings.Join(parts, "\n\n"), nil
	case contract.PrOpened:
		return fmt.Sprintf("PR opened: %s", b.URL), nil
	case contract.OpDecided:
		return fmt.Sprintf("Operation %s: %s. %s", b.ID, b.State, b.Decision.Reason), nil
	case contract.OpRan:
		return fmt.Sprintf("Operation %s: %s. %s\n%s", b.ID, b.State, b.Result.Error, b.Result.Output), nil
	case contract.TurnEnded:
		if b.Outcome == contract.TurnFailed {
			return "turn failed: " + b.Error, nil
		}
		return b.Final, nil
	case contract.ShellJobEnded:
		name := string(b.Job)
		if b.Description != "" {
			name = "'" + b.Description + "'"
		}
		// TODO: legacy/shim remove after the upgrade floor passes an old event migration (D-659).
		result := fmt.Sprintf("exited with code %d", b.ExitCode)
		if b.End != nil {
			switch b.End.Kind {
			case contract.ShellJobExited:
				result = fmt.Sprintf("exited with code %d", b.End.Code)
			case contract.ShellJobSignaled:
				result = fmt.Sprintf("ended by a signal (%s)", b.End.Signal)
			case contract.ShellJobStopped:
				result = "was stopped"
			}
		}
		return fmt.Sprintf("Background job %s %s. Last output:\n%s", name, result, b.Tail), nil
	default:
		return "", fmt.Errorf("event body %T cannot be delivered to a session", body)
	}
}
