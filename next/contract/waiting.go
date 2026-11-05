package contract

import "fmt"

// SessionWaiting is current engine-owned work a session with no running turn is
// waiting on. Owner waits remain urgent; other waits describe background work.
type SessionWaiting struct {
	Kind  WaitKind `json:"kind"`
	Count int      `json:"count"`
	Name  string   `json:"name,omitempty"`
}
type WaitKind string

const (
	WaitOwner    WaitKind = "owner"
	WaitJobs     WaitKind = "jobs"
	WaitChildren WaitKind = "children"
	WaitWorkflow WaitKind = "workflow"
)

func (w SessionWaiting) Label() string {
	switch w.Kind {
	case WaitOwner:
		return "the owner"
	case WaitJobs:
		if w.Count == 1 {
			return "1 job"
		}
		return fmt.Sprintf("%d jobs", w.Count)
	case WaitChildren:
		if w.Count == 1 {
			return "child " + w.Name
		}
		return fmt.Sprintf("%d children", w.Count)
	default:
		if w.Count == 1 {
			return "workflow " + w.Name
		}
		return fmt.Sprintf("%d workflows", w.Count)
	}
}
