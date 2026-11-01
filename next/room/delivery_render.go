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
		if b.ImageUpdated {
			return "Your box image was updated. Your files and conversation are kept. Anything installed outside /box is gone.", nil
		}
		return "Your box was replaced. Your conversation is restored. Unpushed work in the old volume is gone.", nil
	case contract.SessionStarted:
		var parts []string
		if b.Goal != "" {
			parts = append(parts, "Goal: "+b.Goal)
		}
		if b.DoneWhen != "" {
			parts = append(parts, "Done when: "+b.DoneWhen)
		}
		if b.Brief != "" {
			parts = append(parts, b.Brief)
		}
		return strings.Join(parts, "\n\n"), nil
	case contract.GoalSet:
		text := fmt.Sprintf("Goal %s is yours: %s", b.Goal, b.Text)
		if b.DoneWhen != "" {
			text += "\n\nDone when: " + b.DoneWhen
		}
		return text, nil
	case contract.CheckinDue:
		return renderCheckin(b), nil
	case contract.SessionFailed:
		return renderFailures(b), nil
	case contract.SessionClosed:
		goals := make([]string, len(b.Unheld))
		for i, goal := range b.Unheld {
			goals[i] = string(goal)
		}
		return fmt.Sprintf("Session %s closed and left goals with no holder: %s. Hand each to a session with goal set, or drop it with goal done.", b.Session, strings.Join(goals, ", ")), nil
	case contract.HostDiskLow:
		return fmt.Sprintf("Host %s is low on disk: %d GiB free, %d GiB wanted. Close sessions that are done. At the floor, new boxes wait and the oldest stopped boxes close.", b.Host, b.FreeBytes>>30, b.FloorBytes>>30), nil
	case contract.PrOpened:
		return fmt.Sprintf("PR opened: %s", b.URL), nil
	case contract.QuestionAnswered:
		return fmt.Sprintf("Question: %s\n\nAnswer: %s", b.Question, b.Answer), nil
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
		// TODO: legacy/shim remove later — old boxes still send shell.job.ended without End, and saved events lack it. Remove when the engine fills End at the request boundary, every supported box version sends End, and a floor migration converts saved events (D-659).
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

// renderCheckin shows one line per session. Goal text is the sessions' own words, so it stays on one quoted line.
func renderCheckin(b contract.CheckinDue) string {
	lines := []string{"Check-in: these sessions hold open goals and nothing is moving them."}
	for i, child := range b.Children {
		if i == CheckinRows {
			lines = append(lines, fmt.Sprintf("%d more: session list", len(b.Children)-i))
			break
		}
		name := child.Name
		if child.Parent != "" {
			name += " (under " + child.Parent + ")"
		}
		line := fmt.Sprintf("- %s: %s for %s", name, child.State, roundMinutes(child.Idle))
		if child.Reason != "" {
			line += ", " + child.Reason
		}
		if child.Silent {
			line += ", last turn ended with no reply"
		}
		if child.Asking {
			line += ", has an open question for the owner"
		}
		line += fmt.Sprintf(". %s %q", child.Goal, clip(child.Text, 100))
		if child.Open > 0 {
			line += fmt.Sprintf(", %d open items", child.Open)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Quoted text is the sessions' own words. Move each one on, or end its goal. Progress on the goal clears the stall; a stall that stays goes up the tree.")
	return strings.Join(lines, "\n")
}

// renderFailures says what broke and how to repair it. A failed turn keeps its input, so the session needs a new start, not the same words.
func renderFailures(b contract.SessionFailed) string {
	lines := []string{"Sessions failed:"}
	for _, failure := range b.Failures {
		name := failure.Name
		if failure.Parent != "" {
			name += " (under " + failure.Parent + ", which cannot take this)"
		}
		what := "its turn failed; post to it to try again"
		switch failure.Box {
		case contract.BoxFailed:
			what = "its box failed; session restart replaces it"
		case contract.BoxLost:
			what = "its box is lost and its turn is not running; the engine is trying to recover it"
		}
		line := "- " + name + ": " + what
		if failure.Cause != "" {
			line += " (" + string(failure.Cause) + ")"
		}
		if failure.Reason != "" {
			line += fmt.Sprintf(". %q", clip(failure.Reason, 200))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func roundMinutes(d contract.DurationMS) string {
	minutes := int64(d) / 60000
	if minutes < 120 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh", minutes/60)
}

// clip folds text to one line of at most n characters.
func clip(text string, n int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return string(runes)
}
