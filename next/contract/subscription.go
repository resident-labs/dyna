package contract

import "time"

const (
	MethodSubscriptionAdd        Method = "subscription.add"
	MethodSubscriptionList       Method = "subscription.list"
	MethodSubscriptionRemove     Method = "subscription.remove"
	MethodSubscriptionSetEnabled Method = "subscription.set_enabled"
)

// SubscriptionAddRequest creates one subscription (spec 8.5). The engine picks its ID. Exactly one of
// On and Every, and exactly one of Wake and Run, is set. Scope room names Room, scope session names
// Session, and scope machine names neither.
type SubscriptionAddRequest struct {
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`
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

// NoticePosted is an engine notice to a room. The default subscription wakes root on it.
type NoticePosted struct {
	Text string `json:"text"`
}

func (HookFinished) EventType() EventType { return "hook.finished" }
func (HookFinished) isEventBody()         {}
func (HookDue) EventType() EventType      { return "hook.due" }
func (HookDue) isEventBody()              {}
func (NoticePosted) EventType() EventType { return "notice.posted" }
func (NoticePosted) isEventBody()         {}

// OwnedBy names the session that owns a repository event, or nothing when none does. A repository event has
// this method so the default subscription can leave an owned event to its owner and wake root on the rest (spec 7.6).
func (e PrOpened) OwnedBy() SessionID { return e.Session }
