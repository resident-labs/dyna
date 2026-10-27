package room

import (
	"strconv"
	"strings"
)

// GoalChange is what happened to a goal. The web row reads it instead of
// parsing the post text.
type GoalChange string

// Goal changes.
const (
	GoalAdded   GoalChange = "added"
	GoalEdited  GoalChange = "edited"
	GoalAmended GoalChange = "amended"
	// GoalItemsDone tells a goal's holder that a session closed the goal's last
	// open item. Only the holder ends the goal itself.
	GoalItemsDone GoalChange = "items_done"
	GoalFinished  GoalChange = "finished"
	GoalDropped   GoalChange = "dropped"
)

// goalChanges lists every goal change the web needs as a union type.
var goalChanges = []GoalChange{GoalAdded, GoalEdited, GoalAmended, GoalItemsDone, GoalFinished, GoalDropped}

// GoalState is where the goal stands after the event.
type GoalState string

// Goal states.
const (
	GoalStateActive  GoalState = "active"
	GoalStateDone    GoalState = "done"
	GoalStateDropped GoalState = "dropped"
)

// goalStates lists every goal state the web needs as a union type.
var goalStates = []GoalState{GoalStateActive, GoalStateDone, GoalStateDropped}

// GoalEvent tells the owner feed that a goal changed. The owner feed is its
// only recipient, so it wakes nobody.
type GoalEvent struct {
	Type   GoalChange `json:"type"`
	GoalID string     `json:"goalId"`
	// Title is the goal text, clipped as the feed row needs it.
	Title string    `json:"title"`
	Actor string    `json:"actor"`
	State GoalState `json:"state"`
	// GoalItemsDone records how many times the goal's last open item closed.
	// It is part of the post identity, so a reopen and a second close notify
	// the holders again while a retried post keeps the post it made.
	ItemsClosed int `json:"itemsClosed,omitempty"`
	// Summary is the finishing note of a finished goal, or the text of a brief
	// amendment.
	Summary string `json:"summary,omitempty"`
	// Amendment is how many amendments the goal holds after this one, oldest
	// first. It is part of the post identity, so the same words amended twice
	// still deliver twice.
	Amendment int `json:"amendment,omitempty"`
}

func (GoalEvent) eventFamily() EventFamily { return FamilyGoal }
func (g GoalEvent) eventType() EventType   { return EventType(g.Type) }

func (g GoalEvent) eventSubject() string { return g.GoalID }

// eventDedupe keys one post per goal and change and text, so a retried edit
// posts once and a second edit posts again. An amendment adds its stored
// position and a closed checklist adds its close number: two posts with the
// same words are two different posts.
func (g GoalEvent) eventDedupe() string {
	key := strings.Join([]string{string(g.Type), g.Title, g.Summary, strconv.Itoa(g.Amendment)}, "\x00")
	if g.ItemsClosed > 0 {
		key += "\x00" + strconv.Itoa(g.ItemsClosed)
	}
	return key
}
