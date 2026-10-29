package room

import "strconv"

// WorktreeReason is the class of a kept worktree. It keys the dedupe of the
// notice and picks the wording of the card.
type WorktreeReason string

// Worktree reasons.
const (
	WorktreeNotMerged     WorktreeReason = "not-merged"
	WorktreeDirty         WorktreeReason = "dirty"
	WorktreeManualHandoff WorktreeReason = "manual-handoff"
	WorktreeKeepFailed    WorktreeReason = "keep-failed"
	WorktreeOther         WorktreeReason = "other"
)

// worktreeReasons lists every reason the web needs as a union type.
var worktreeReasons = []WorktreeReason{
	WorktreeNotMerged, WorktreeDirty, WorktreeManualHandoff, WorktreeKeepFailed, WorktreeOther,
}

// WorktreeEvent tells a session that cleanup kept one of its worktrees, with
// the facts the session needs to act on it. An empty Path means cleanup could
// not keep the session's trees at all.
type WorktreeEvent struct {
	// Owner is the session that owns the tree, or the session being closed
	// when cleanup could not keep the trees.
	Owner  string `json:"owner"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Ahead is the number of commits not on the base. Zero means unknown.
	Ahead  int            `json:"ahead,omitempty"`
	Dirty  []string       `json:"dirty,omitempty"`
	Reason WorktreeReason `json:"reason"`
	// Detail is the full sentence cleanup gave, when it says more than the
	// reason class does.
	Detail string `json:"detail,omitempty"`
}

func (WorktreeEvent) eventFamily() EventFamily { return FamilyWorktree }
func (WorktreeEvent) eventType() EventType     { return TypeKept }
func (w WorktreeEvent) eventSubject() string   { return w.Path }

// eventDedupe keys one post per owner, tree and reason class: a later pass
// that finds the same tree with other counts posts nothing, a new reason does.
func (w WorktreeEvent) eventDedupe() string {
	return w.Owner + "\x00" + w.Path + "\x00" + string(w.Reason)
}

// WorktreePortsEvent tells every room that the host-wide worktree port pool
// is close to exhaustion. Generation identifies one continuous high-capacity
// period, and Threshold is the crossed percentage of the pool.
type WorktreePortsEvent struct {
	Used       int `json:"used"`
	Limit      int `json:"limit"`
	Threshold  int `json:"threshold"`
	Generation int `json:"generation"`
}

func (WorktreePortsEvent) eventFamily() EventFamily { return FamilyMachine }
func (WorktreePortsEvent) eventType() EventType     { return TypeWorktreePorts }
func (WorktreePortsEvent) eventSubject() string     { return "worktree-ports" }

// eventDedupe permits one 90% and one 95% alert in each high-capacity period.
func (w WorktreePortsEvent) eventDedupe() string {
	return strconv.Itoa(w.Generation) + "\x00" + strconv.Itoa(w.Threshold)
}
