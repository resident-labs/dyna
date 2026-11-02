package contract

import "time"

const (
	MethodSubscriptionAdd        Method = "subscription.add"
	MethodSubscriptionList       Method = "subscription.list"
	MethodSubscriptionRemove     Method = "subscription.remove"
	MethodSubscriptionSetEnabled Method = "subscription.set_enabled"
)

type SubscriptionScope string

const (
	ScopeMachine SubscriptionScope = "machine"
	ScopeRoom    SubscriptionScope = "room"
	ScopeSession SubscriptionScope = "session"
)

// Subscription is a hook as a typed event subscription (spec 8.5). Exactly
// one of On and Every, and exactly one of Wake and Run, is set.
type Subscription struct {
	ID      SubscriptionID    `json:"id"`
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`
	Where   []string          `json:"where,omitempty"`
	Every   DurationMS        `json:"every,omitempty"`
	Wake    *Principal        `json:"wake,omitempty"`
	Run     *RunSpec          `json:"run,omitempty"`
}

type RunSpec struct {
	Command []string    `json:"command"`
	Image   string      `json:"image"`
	Size    BoxSizeName `json:"size"`
	Host    HostName    `json:"host,omitempty"` // Every Run goes to this host and waits for it. File locks need this, as they stop at a host.
	Files   []RunFile   `json:"files,omitempty"`
}

type RunFile struct {
	Path string `json:"path"` // inside the box
	Body string `json:"body"`
}

// SubscriptionAddRequest creates one subscription (spec 8.5). The engine picks its ID. Exactly one of
// On and Every, and exactly one of Wake and Run, is set. Scope room names Room, scope session names
// Session, and scope machine names neither. Where filters On, as key=value, key!=value and text~value, all of which must hold.
type SubscriptionAddRequest struct {
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`
	Where   []string          `json:"where,omitempty"`
	Every   DurationMS        `json:"every,omitempty"`
	Wake    *Principal        `json:"wake,omitempty"`
	Run     *RunSpec          `json:"run,omitempty"`
}
type SubscriptionAddResponse struct {
	ID SubscriptionID `json:"id"`
}

// SubscriptionListRequest lists a room's subscriptions, its sessions' included. With no room the owner
// gets the machine's.
type SubscriptionListRequest struct {
	Room RoomID `json:"room,omitempty"`
}
type SubscriptionListResponse struct {
	Subscriptions []SubscriptionInfo `json:"subscriptions"`
}

// SubscriptionInfo is one subscription and what it is doing.
type SubscriptionInfo struct {
	Subscription Subscription `json:"subscription"`
	Disabled     bool         `json:"disabled,omitempty"`
	Default      bool         `json:"default,omitempty"` // the room's own subscription for root
	Running      bool         `json:"running,omitempty"`
	Failures     int          `json:"failures,omitempty"` // Run failures in a row
	NextAt       *time.Time   `json:"nextAt,omitempty"`   // the next firing, counting any backoff
	LastAt       *time.Time   `json:"lastAt,omitempty"`
	Last         HookResult   `json:"last,omitempty"`
}

type SubscriptionRemoveRequest struct {
	ID SubscriptionID `json:"id"`
}
type SubscriptionRemoveResponse struct{}

type SubscriptionSetEnabledRequest struct {
	ID      SubscriptionID `json:"id"`
	Enabled bool           `json:"enabled"`
}
type SubscriptionSetEnabledResponse struct{}

type HookResult string

const (
	HookPassed  HookResult = "passed"
	HookFailed  HookResult = "failed"
	HookStopped HookResult = "stopped" // its subscription or room went while it ran
)

// HookFinished ends one Run (spec 8.5) and wakes nobody. Output is the end of what the command wrote, and
// it is untrusted text.
type HookFinished struct {
	Subscription SubscriptionID `json:"subscription"`
	Result       HookResult     `json:"result"`
	ExitCode     int            `json:"exitCode"`
	Cause        FailureCause   `json:"cause,omitempty"` // why a failed Run has no exit code of its own
	Output       string         `json:"output,omitempty"`
	Failures     int            `json:"failures,omitempty"` // failures in a row, counting this one
	RetryAt      *time.Time     `json:"retryAt,omitempty"`  // the end of the backoff
}

// HookDue is what a scheduled Wake delivers, since a schedule has no event of its own.
type HookDue struct {
	Subscription SubscriptionID `json:"subscription"`
}

// NoticePosted is an engine notice to a room, which the default subscription wakes root on.
type NoticePosted struct {
	Text string `json:"text"`
}

func (HookFinished) EventType() EventType { return "hook.finished" }
func (HookFinished) isEventBody()         {}
func (HookDue) EventType() EventType      { return "hook.due" }
func (HookDue) isEventBody()              {}
func (NoticePosted) EventType() EventType { return "notice.posted" }
func (NoticePosted) isEventBody()         {}
