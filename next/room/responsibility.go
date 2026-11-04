package room

import (
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

var responsibilityLabels = map[contract.ResponsibilityKind]string{
	contract.ResponsibilityTurn:     "turn",
	contract.ResponsibilityShellJob: "shell job",
	contract.ResponsibilityTerminal: "terminal",
	contract.ResponsibilityWorkflow: "workflow run",
	contract.ResponsibilityGoal:     "goal",
	contract.ResponsibilityPR:       "PR",
	contract.ResponsibilityIssue:    "issue",
	contract.ResponsibilityQuestion: "question",
	contract.ResponsibilityChild:    "child",
}

// ResponsibilityName words one item of a session's list as a plain fact, such as "shell job job_7f3b".
func ResponsibilityName(item contract.Responsibility) string {
	label := responsibilityLabels[item.Kind]
	if item.ID == "" {
		return label
	}
	return label + " " + item.ID
}

// withContinue ends a restart notice with the instruction to carry on when the session holds an open goal, and leaves the facts
// alone when it does not. This is the owner's exception (2026-11-04) to wording notices as facts.
func withContinue(facts string, openGoal bool) string {
	if !openGoal {
		return facts
	}
	return facts + " Continue the work you were doing: check `git status` and your goal, reinstall anything you need, and keep going until the goal is done or you are blocked. Don't end your turn just to report progress."
}

// BoxRestartedNotice tells a session that asked to restart its own box that it has.
func BoxRestartedNotice(openGoal bool) string {
	return withContinue("Your box restarted on the current version. Files under /box and this conversation are kept.", openGoal)
}

// BoxUpdateNotice tells a session what holds its box on an old binary. It is empty when nothing outlasts the turn: the box then
// stops as the turn ends, before anything could read this. A crew's run ends with its turn, so it counts as that turn.
func BoxUpdateNotice(held []contract.Responsibility) string {
	var names []string
	turn := false
	for _, item := range held {
		switch item.Kind {
		case contract.ResponsibilityTurn:
			turn = true
		case contract.ResponsibilityShellJob:
			if item.ID == "" {
				names = append(names, "a shell job")
			} else {
				names = append(names, item.ID)
			}
		case contract.ResponsibilityTerminal:
			names = append(names, "your open terminal")
		}
	}
	if len(names) == 0 {
		return ""
	}
	if turn {
		names = append(names, "your open turn")
	}
	end := "ends"
	list := names[len(names)-1]
	if len(names) > 1 {
		end = "end"
		list = strings.Join(names[:len(names)-1], ", ") + " and " + list
	}
	return fmt.Sprintf("An update is ready for your box. It applies when %s %s. Your files and conversation are kept.", list, end)
}

// ResponsibilityNames words several items as one list.
func ResponsibilityNames(items []contract.Responsibility) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = ResponsibilityName(item)
	}
	return strings.Join(names, ", ")
}

// GoalNames joins goal IDs for a notice.
func GoalNames(goals []contract.GoalID) string {
	names := make([]string, len(goals))
	for i, goal := range goals {
		names[i] = string(goal)
	}
	return strings.Join(names, ", ")
}
