package contract

import "time"

const (
	MethodGoalSet     Method = "goal.set"
	MethodGoalList    Method = "goal.list"
	MethodGoalItem    Method = "goal.item"
	MethodGoalDone    Method = "goal.done"
	MethodGoalJournal Method = "goal.journal"
)

type GoalState string

const (
	GoalActive  GoalState = "active"
	GoalDone    GoalState = "done"
	GoalDropped GoalState = "dropped"
)

type GoalItemState string

const (
	GoalItemOpen    GoalItemState = "open"
	GoalItemDone    GoalItemState = "done"
	GoalItemDropped GoalItemState = "dropped"
)

type GoalItem struct {
	Number int           `json:"number"`
	Text   string        `json:"text"`
	State  GoalItemState `json:"state"`
}

// Goal is one piece of work with a done condition. Session is its holder; a goal with no holder waits for root to assign it.
type Goal struct {
	ID       GoalID     `json:"id"`
	Room     RoomID     `json:"room"`
	Session  SessionID  `json:"session,omitempty"`
	Text     string     `json:"text"`
	DoneWhen string     `json:"doneWhen,omitempty"`
	State    GoalState  `json:"state"`
	Summary  string     `json:"summary,omitempty"` // how it ended
	Items    []GoalItem `json:"items"`
	Created  time.Time  `json:"created"`
	Updated  time.Time  `json:"updated"`
}

// GoalSetRequest creates a goal, or with Goal set edits one or hands it to another holder.
// An empty Room means the caller's room; an empty Session on a create means the caller.
type GoalSetRequest struct {
	ClientID string    `json:"clientId,omitempty"`
	Room     RoomID    `json:"room,omitempty"`
	Goal     GoalID    `json:"goal,omitempty"`
	Session  SessionID `json:"session,omitempty"`
	Text     string    `json:"text,omitempty"`
	DoneWhen string    `json:"doneWhen,omitempty"`
}
type GoalSetResponse struct {
	Goal GoalID `json:"goal"`
}

// GoalListRequest lists a room's active goals, or with All its ended ones too. Session narrows the list to one holder.
type GoalListRequest struct {
	Room    RoomID    `json:"room,omitempty"`
	Session SessionID `json:"session,omitempty"`
	All     bool      `json:"all,omitempty"`
}
type GoalListResponse struct {
	Goals []Goal `json:"goals"`
}

// GoalItemRequest adds an item, or with Item set changes that item's text or state.
type GoalItemRequest struct {
	Room  RoomID        `json:"room,omitempty"`
	Goal  GoalID        `json:"goal"`
	Item  int           `json:"item,omitempty"`
	Text  string        `json:"text,omitempty"`
	State GoalItemState `json:"state,omitempty"`
}
type GoalItemResponse struct {
	Item int `json:"item"`
}

// GoalDoneRequest ends a goal as done, or as dropped with Drop.
type GoalDoneRequest struct {
	Room    RoomID `json:"room,omitempty"`
	Goal    GoalID `json:"goal"`
	Summary string `json:"summary"`
	Drop    bool   `json:"drop,omitempty"`
}
type GoalDoneResponse struct{}

// GoalJournalRequest adds Text to a goal's journal. Without Text it only reads.
type GoalJournalRequest struct {
	Room RoomID `json:"room,omitempty"`
	Goal GoalID `json:"goal"`
	Text string `json:"text,omitempty"`
}
type GoalJournalEntry struct {
	At   time.Time `json:"at"`
	By   Principal `json:"by"`
	Text string    `json:"text"`
}

// GoalJournalResponse holds the newest entries first.
type GoalJournalResponse struct {
	Entries []GoalJournalEntry `json:"entries"`
}

// GoalSet records a new goal, an edit or a new holder. It wakes a holder that someone else handed the goal to.
type GoalSet struct {
	Goal     GoalID    `json:"goal"`
	Session  SessionID `json:"session,omitempty"`
	Text     string    `json:"text"`
	DoneWhen string    `json:"doneWhen,omitempty"`
}

type GoalItemChanged struct {
	Goal  GoalID        `json:"goal"`
	Item  int           `json:"item"`
	Text  string        `json:"text"`
	State GoalItemState `json:"state"`
}

type GoalEnded struct {
	Goal    GoalID    `json:"goal"`
	Session SessionID `json:"session,omitempty"`
	State   GoalState `json:"state"`
	Summary string    `json:"summary"`
}

func (GoalSet) EventType() EventType         { return "goal.set" }
func (GoalSet) isEventBody()                 {}
func (GoalItemChanged) EventType() EventType { return "goal.item_changed" }
func (GoalItemChanged) isEventBody()         {}
func (GoalEnded) EventType() EventType       { return "goal.ended" }
func (GoalEnded) isEventBody()               {}

type CheckinState string

const (
	CheckinIdle    CheckinState = "idle"
	CheckinStopped CheckinState = "stopped"
	CheckinBlocked CheckinState = "blocked"
)

// CheckinChild is one session a check-in reports: it holds an open goal and nothing is moving it.
type CheckinChild struct {
	Session SessionID    `json:"session"`
	Name    string       `json:"name"`
	Parent  string       `json:"parent,omitempty"` // its parent's name, when the report went past that parent
	State   CheckinState `json:"state"`
	Reason  string       `json:"reason,omitempty"`
	Idle    DurationMS   `json:"idle"`
	Silent  bool         `json:"silent,omitempty"` // its last turn ended with no reply to anyone
	Asking  bool         `json:"asking,omitempty"` // it has an open question for the owner
	Goal    GoalID       `json:"goal"`
	Text    string       `json:"text"`
	Open    int          `json:"open"` // the goal's open items
}

// CheckinDue is one report to Session. It is an engine notice: it wakes Session and owes no reply.
type CheckinDue struct {
	Session  SessionID      `json:"session"`
	Children []CheckinChild `json:"children"`
}

func (CheckinDue) EventType() EventType { return "checkin.due" }
func (CheckinDue) isEventBody()         {}

// SessionFailure is one session whose turn failed or whose box is failed or lost.
type SessionFailure struct {
	Session SessionID    `json:"session"`
	Name    string       `json:"name"`
	Parent  string       `json:"parent,omitempty"` // its parent's name, when the notice went past that parent
	Box     BoxState     `json:"box,omitempty"`    // failed or lost; empty for a failed turn
	Cause   FailureCause `json:"cause,omitempty"`
	Reason  string       `json:"reason,omitempty"`
}

// SessionFailed is one notice to To, sent as soon as the engine sees the failure. It wakes To and owes no reply.
type SessionFailed struct {
	To       SessionID        `json:"to"`
	Failures []SessionFailure `json:"failures"`
}

func (SessionFailed) EventType() EventType { return "session.failed" }
func (SessionFailed) isEventBody()         {}
