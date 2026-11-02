package room

import (
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

// ResponsibilityNames words several items as one list.
func ResponsibilityNames(items []contract.Responsibility) string {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = ResponsibilityName(item)
	}
	return strings.Join(names, ", ")
}
