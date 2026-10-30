// Package sessionclose defines the dependency-free session close recovery contract.
package sessionclose

// ResponsibilityKind identifies one kind of owned work.
type ResponsibilityKind string

const (
	ResponsibilityKindPR       ResponsibilityKind = "pr"
	ResponsibilityKindGoal     ResponsibilityKind = "goal"
	ResponsibilityKindItem     ResponsibilityKind = "item"
	ResponsibilityKindQuestion ResponsibilityKind = "question"
	ResponsibilityKindChild    ResponsibilityKind = "child"
	ResponsibilityKindShell    ResponsibilityKind = "shell"
	ResponsibilityKindWorkflow ResponsibilityKind = "workflow"
)

// Responsibility is one thing a session owns.
type Responsibility struct {
	Kind ResponsibilityKind `json:"kind"`
	ID   string             `json:"id"`
	// Text names the item and the command that ends or moves it.
	Text string `json:"text"`
}

// Reason identifies why a session close was refused.
type Reason string

const (
	ReasonResponsibilities Reason = "responsibilities"
	ReasonGoalNeedsAgent   Reason = "goal-needs-agent"
)

// Action is one recovery the server allows for a refused close.
type Action string

const (
	ActionRetry Action = "retry"
	ActionForce Action = "force-close"
)

// Recovery tells clients which close recovery actions are allowed and which
// responsibilities remain. Clients must use Actions, not error prose, to
// choose their controls.
type Recovery struct {
	Reason  Reason           `json:"reason"`
	Actions []Action         `json:"actions" tstype:"Action[] | null"`
	Items   []Responsibility `json:"items" tstype:"Responsibility[] | null"`
}
