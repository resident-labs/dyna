// Package sessionclose defines the dependency-free session close recovery contract.
package sessionclose

// Responsibility is one thing a session owns.
type Responsibility struct {
	// Kind is pr, goal, item, question, child, shell or workflow.
	Kind string `json:"kind"`
	ID   string `json:"id"`
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
	Actions []Action         `json:"actions"`
	Items   []Responsibility `json:"items"`
}
