package room

import (
	"strconv"
)

// CheckinState is a child's activity when the check-in was written.
type CheckinState string

// Check-in states. They are the session states a parent acts on.
const (
	CheckinRunning CheckinState = "running"
	CheckinQueued  CheckinState = "queued"
	CheckinIdle    CheckinState = "idle"
)

// checkinStates lists every check-in state the web needs as a union type.
var checkinStates = []CheckinState{CheckinRunning, CheckinQueued, CheckinIdle}

// CheckinAttention identifies check-ins that need separate attention.
type CheckinAttention string

const (
	CheckinAttentionNone    CheckinAttention = "none"
	CheckinAttentionBlocked CheckinAttention = "blocked"
	CheckinAttentionOverdue CheckinAttention = "overdue"
)

var checkinAttentions = []CheckinAttention{CheckinAttentionNone, CheckinAttentionBlocked, CheckinAttentionOverdue}

// CheckinCounts is how many children were in each state.
type CheckinCounts struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Idle    int `json:"idle"`
}

// CheckinChild is one child in a check-in, with the facts a parent acts on.
type CheckinChild struct {
	Name  string       `json:"name"`
	State CheckinState `json:"state"`
	// Age is how long the child has been in this state, as "12m" or "<1m".
	Age string `json:"age"`
	// Goal is the work the child holds, or "no open goal or item".
	Goal string `json:"goal"`
	// PR names the child's open pull requests, or "no open PRs".
	PR string `json:"pr"`
}

// CheckinEvent is a parent check-in: the stalest children first, so the
// parent reads the ones most likely to be stuck before the rest.
//
// It wakes the parent and no one else, as the free-text check-in did.
type CheckinEvent struct {
	Parent string `json:"parent"`
	// Attention is absent in check-ins written before this field existed.
	Attention CheckinAttention `json:"attention,omitempty"`
	// WindowStart is when the check-in window opened, in unix milliseconds.
	// It keys the post of a window, so a retried delivery posts once.
	WindowStart int64          `json:"windowStart"`
	Counts      CheckinCounts  `json:"counts"`
	Children    []CheckinChild `json:"children"`
	// Detail is the child table the agent reads, one row per child.
	Detail string `json:"detail,omitempty"`
}

func (CheckinEvent) eventFamily() EventFamily { return FamilyCheckin }
func (CheckinEvent) eventType() EventType     { return TypeSummary }
func (c CheckinEvent) eventSubject() string   { return c.Parent }

func (c CheckinEvent) eventDedupe() string { return strconv.FormatInt(c.WindowStart, 10) }
