package sessionclose

import "fmt"

// OutcomeResult is what a close did with one piece of work.
type OutcomeResult string

const (
	ResultMoved     OutcomeResult = "moved"     // handed to another session
	ResultClosed    OutcomeResult = "closed"    // a child that owned nothing
	ResultCanceled  OutcomeResult = "canceled"  // a workflow run
	ResultWithdrawn OutcomeResult = "withdrawn" // a question
	ResultReturned  OutcomeResult = "returned"  // a goal item, to its goal
	ResultStopped   OutcomeResult = "stopped"   // a shell, unless it is kept
	ResultReleased  OutcomeResult = "released"  // a reviewer role, to the pool
	ResultDeferred  OutcomeResult = "deferred"  // a release that waits for a verdict
	ResultFailed    OutcomeResult = "failed"    // the step could not run
)

// Failure names why one close step failed, so a client words it and offers
// only the choices the server allows.
type Failure string

const (
	FailureHandoff        Failure = "handoff"          // the work did not move
	FailureGoalNeedsAgent Failure = "goal-needs-agent" // no open agent can hold the goal
	FailureChildHeir      Failure = "child-heir"       // the children's heir was not found
	FailureChildHandoff   Failure = "child-handoff"    // a child did not move
	FailureProcessCleanup Failure = "process-cleanup"  // the native process did not stop
)

// Outcome is one typed fact of a session close: the work it concerns, what
// happened to it, and where the work went. Clients draw labels, links and
// choices from these facts; no sentence of the engine's reaches them.
type Outcome struct {
	// Kind names the kind of work, using the same vocabulary as a
	// responsibility. It is empty when the failure concerns the close itself
	// rather than one piece of work.
	Kind   ResponsibilityKind `json:"kind,omitempty"`
	Result OutcomeResult      `json:"result"`
	// ID names the work: a goal, item, run, question or shell ID, or a
	// session ID for a child.
	ID string `json:"id,omitempty"`
	// Number is the PR number of PR work.
	Number int `json:"number,omitempty"`
	// Name is the display name of the work, such as a child session's name.
	Name string `json:"name,omitempty"`
	// To is the address of the session that took the work. It is empty when
	// the work became top level.
	To string `json:"to,omitempty"`
	// Failure names why the step did not run. A client words the failure and
	// offers the choice it makes possible; the engine sends no sentence.
	Failure Failure `json:"failure,omitempty"`
}

// Failed reports whether the step did not run.
func (o Outcome) Failed() bool { return o.Result == ResultFailed }

// Text is the line the terminal and the session journal show for one outcome.
// The web app words the same facts from the typed fields, so this wording
// reaches no view it draws.
func (o Outcome) Text() string {
	subject := o.subject()
	switch o.Result {
	case ResultMoved:
		return subject + " → " + o.destination()
	case ResultClosed:
		return subject + " closed"
	case ResultCanceled:
		return subject + " canceled"
	case ResultWithdrawn:
		return subject + " withdrawn"
	case ResultReturned:
		return subject + " returned to its goal"
	case ResultStopped:
		return subject + " stops unless kept"
	case ResultReleased:
		return subject + " reviewer released to the pool"
	case ResultDeferred:
		return subject + " reviewer release waits for its saved verdict"
	case ResultFailed:
		return subject + " failed: " + string(o.Failure)
	}
	return subject
}

// subject names the work of one outcome in the terminal's words.
func (o Outcome) subject() string {
	switch o.Kind {
	case ResponsibilityPR:
		return fmt.Sprintf("PR #%d", o.Number)
	case ResponsibilityGoal:
		return "Goal " + o.ID
	case ResponsibilityItem:
		return "Goal item " + o.ID
	case ResponsibilityQuestion:
		return "Question " + o.ID
	case ResponsibilityChild:
		return "Child " + o.Name
	case ResponsibilityShell:
		return "Shell " + o.ID
	case ResponsibilityWorkflow:
		return "Workflow " + o.ID
	}
	return "Close"
}

// destination names where the work went, or that it became top level.
func (o Outcome) destination() string {
	if o.To == "" {
		return "top level"
	}
	return o.To
}
