package contract

// MethodSessionResponsibilities reads what a session still holds and what a close would do with it.
const MethodSessionResponsibilities Method = "session.responsibilities"

// ResponsibilityKind is one thing a session can still hold. engine/store/responsibilities.go defines what each kind means.
type ResponsibilityKind string

const (
	ResponsibilityTurn     ResponsibilityKind = "turn"
	ResponsibilityShellJob ResponsibilityKind = "shell_job"
	ResponsibilityTerminal ResponsibilityKind = "terminal"
	ResponsibilityWorkflow ResponsibilityKind = "workflow"
	ResponsibilityGoal     ResponsibilityKind = "goal"
	ResponsibilityPR       ResponsibilityKind = "pr"
	ResponsibilityIssue    ResponsibilityKind = "issue"
	ResponsibilityQuestion ResponsibilityKind = "question"
	ResponsibilityChild    ResponsibilityKind = "child"
)

type ResponsibilityGroup string

const (
	ResponsibilityNeedsBox ResponsibilityGroup = "needs_box" // something runs in the box, so it must stay up
	ResponsibilityOwesWork ResponsibilityGroup = "owes_work" // work is still due, which needs no box until it moves
)

// Responsibility is one item of a session's list.
type Responsibility struct {
	Kind  ResponsibilityKind  `json:"kind"`
	Group ResponsibilityGroup `json:"group"`
	// ID names the item: g27, job_7f3b, org/repo#6216 or a child's name. A terminal has none, and neither has a shell job
	// of a box agent too old to name its jobs.
	ID      string `json:"id,omitempty"`
	Blocked bool   `json:"blocked,omitempty"` // a question that left the session nothing safe to do until the answer
}

type SessionResponsibilitiesRequest struct {
	Session SessionID `json:"session"`
}
type SessionResponsibilitiesResponse struct {
	Items []Responsibility     `json:"items"`
	Close SessionCloseResponse `json:"close"` // what closing the session now would do; empty for root, which cannot close
}
