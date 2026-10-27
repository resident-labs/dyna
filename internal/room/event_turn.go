package room

import (
	"strconv"
)

// TurnState is how a session turn ended.
type TurnState string

// Turn states.
const (
	TurnCompleted TurnState = "completed"
	TurnFailed    TurnState = "failed"
	TurnStopped   TurnState = "stopped"
)

// turnStates lists every turn state the web needs as a union type.
var turnStates = []TurnState{TurnCompleted, TurnFailed, TurnStopped}

// TurnEvent is the end of one session turn. Its summary is the report the
// session gave, which the completion row shows as its body.
type TurnEvent struct {
	// Session is the address of the session that ended the turn, as the post
	// reports it, and SessionID its exact session.
	Session   string    `json:"session"`
	SessionID string    `json:"sessionId"`
	Turn      int       `json:"turn"`
	State     TurnState `json:"state"`
	// Summary is the report the session gave, as markdown. A failed turn has
	// the error instead.
	Summary string `json:"summary,omitempty"`
	// Error is why the turn failed, when it did.
	Error string `json:"error,omitempty"`
	// WorkerStderr is the failed worker's standard-error tail, diagnostic
	// evidence the card discloses on its own, never the cause (D-108).
	WorkerStderr string `json:"workerStderr,omitempty"`
	// Detail is the rest of the line an agent reads: how to inspect the turn
	// or recover from it.
	Detail string `json:"detail,omitempty"`
}

func (TurnEvent) eventFamily() EventFamily { return FamilyTurn }
func (t TurnEvent) eventSubject() string   { return t.Session }

// eventType reads the state: each end of a turn has its own type, so a card
// and a feed view pick their tone from the registry.
func (t TurnEvent) eventType() EventType {
	switch t.State {
	case TurnFailed:
		return TypeTurnFailed
	case TurnStopped:
		return TypeTurnStopped
	}
	return TypeTurnCompleted
}

// eventDedupe keys one post per session and turn, so a retried report keeps
// its post.
func (t TurnEvent) eventDedupe() string { return strconv.Itoa(t.Turn) + "\x00" + string(t.State) }
