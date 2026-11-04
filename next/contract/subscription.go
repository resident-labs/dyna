package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

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
// one of On and Every, and exactly one of Wake and Run, is set. Wake is the ID of the session it wakes.
type Subscription struct {
	ID      SubscriptionID    `json:"id"`
	Scope   SubscriptionScope `json:"scope"`
	Room    RoomID            `json:"room,omitempty"`
	Session SessionID         `json:"session,omitempty"`
	On      []EventType       `json:"on,omitempty"`
	Where   []string          `json:"where,omitempty"`
	Every   Duration          `json:"every,omitempty"`
	Wake    SessionID         `json:"wake,omitempty"`
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
// Session, and scope machine names neither room nor session. Session and Wake each take a session's name in Room, or in the
// caller's room with no Room, or its ID. Where filters On, as key=value, key!=value and text~value, all of which must hold.
type SubscriptionAddRequest struct {
	Scope   SubscriptionScope `json:"scope" desc:"What the subscription belongs to: session, room or machine. A session's subscription ends when the session closes. Room scope is root's, and machine scope is the owner's."`
	Room    RoomID            `json:"room,omitempty" desc:"A room_… ID, from your instructions. Omitted, it is your own room."`
	Session string            `json:"session,omitempty" desc:"With scope session, the session it belongs to: a name in the room, such as upstream-sync, or a ses_… ID."`
	On      []EventType       `json:"on,omitempty" desc:"Event types that fire it, such as github.pr.opened; dyna subscription events lists them. Give on or every."`
	Where   []string          `json:"where,omitempty" desc:"Filters on github.* and git.* events in on: key=value, key!=value or text~value, with key repo, author, actor, label, kind or number. kind is a change kind of github.pr.changed or github.issue.changed, such as pushed, ci_failed or merged. All must hold."`
	Every   Duration          `json:"every,omitempty" desc:"A schedule, as a duration string such as 1h, 15m or 90s, of at least 1m. Give on or every."`
	Wake    string            `json:"wake,omitempty" desc:"The session to wake: a name in the room, such as upstream-sync, or a ses_… ID. Give wake or run."`
	Run     *RunSpec          `json:"run,omitempty" desc:"A command for a small box: command, image (base, or an image folder of the room's default repository), size (small or agent), and optional host and files. Owner or root only. Give wake or run."`
}

// UnmarshalJSON refuses a number for every and an object for session or wake, naming the field and the form it takes.
func (r *SubscriptionAddRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["every"]; ok && !jsonString(raw) {
		return fmt.Errorf(`every is a duration string such as "1h", "15m" or "90s", not %s`, rawKind(raw))
	}
	for _, name := range []string{"session", "wake"} {
		if raw, ok := fields[name]; ok && !jsonString(raw) {
			return fmt.Errorf(`%s is a session name or ID as a string, such as "upstream-sync" or "ses_…", not %s`, name, rawKind(raw))
		}
	}
	type plain SubscriptionAddRequest
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	*r = SubscriptionAddRequest(v)
	return nil
}

// jsonString says whether raw is a string or null, which is the field left out.
func jsonString(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"' || string(raw) == "null"
}

// rawKind names a JSON value that is not a string: a number or a boolean as written, or an object or an array.
func rawKind(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) > 0 && raw[0] == '{':
		return "an object"
	case len(raw) > 0 && raw[0] == '[':
		return "an array"
	case string(raw) == "true", string(raw) == "false":
		return string(raw)
	}
	return "the number " + string(raw)
}

type SubscriptionAddResponse struct {
	ID SubscriptionID `json:"id"`
}

// SubscriptionListRequest lists a room's subscriptions, its sessions' included. With no room the owner
// gets the machine's.
type SubscriptionListRequest struct {
	Room RoomID `json:"room,omitempty" desc:"A room_… ID, from your instructions. Omitted, it is your own room."`
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
// it reaches a session as outside text (decision 13).
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
