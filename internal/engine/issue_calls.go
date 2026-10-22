package engine

import "dyna-agent/internal/websvc"

// issueCalls share the CLI's owner-or-root triage policy.
var issueCalls = []string{"CloseIssue", "IssueGoal", "ExportIssue"}

func protectIssueCalls(calls map[string]websvc.Call) {
	protectCalls(calls, issueCalls, "triaging room issues", nil)
}
