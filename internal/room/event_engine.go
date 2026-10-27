package room

import (
	"strconv"
)

// EngineEvent is a note from the engine itself. The timed flush uses it to
// deliver the quiet posts a room held for its root seat.
type EngineEvent struct {
	// Minutes is how long the room held quiet posts for the root seat.
	Minutes int `json:"minutes"`
	// Detail is the cause, when the engine has one to add.
	Detail string `json:"detail,omitempty"`
}

func (EngineEvent) eventFamily() EventFamily { return FamilyEngine }
func (EngineEvent) eventType() EventType     { return TypeRootFlush }

// eventSubject keeps the flush out of any other engine note's rows.
func (EngineEvent) eventSubject() string { return "flush" }

func (e EngineEvent) eventDedupe() string { return strconv.Itoa(e.Minutes) }
