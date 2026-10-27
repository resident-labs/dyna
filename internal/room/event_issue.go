package room

import (
	"strconv"
)

// The issue family posts one event per fact about a room issue. Every filer
// ends in room.Store.FileIssue, so the librarian's issue, an agent's and the
// owner's look the same and differ only in who filed them.
const FamilyIssue EventFamily = "issue"

// IssueChange is what happened to an issue. The web card picks its state chip
// from the event, never from the text.
type IssueChange string

// Issue changes.
const (
	IssueFiled   IssueChange = "filed"
	IssueUpdated IssueChange = "updated"
	IssueClosed  IssueChange = "closed"
	// IssueNoted adds evidence to an issue without changing its state.
	IssueNoted IssueChange = "noted"
)

// issueChanges lists every issue change the web needs as a union type.
var issueChanges = []IssueChange{IssueFiled, IssueUpdated, IssueClosed, IssueNoted}

// IssueState is where an issue stands after the event. It is the state the
// room store records, so the card and the Issues panel cannot disagree.
type IssueState string

// Issue states.
const (
	IssueStateOpen     IssueState = "open"
	IssueStateGoal     IssueState = "goal"
	IssueStateExported IssueState = "exported"
	IssueStateClosed   IssueState = "closed"
)

// issueStates lists every issue state the web needs as a union type.
var issueStates = []IssueState{IssueStateOpen, IssueStateGoal, IssueStateExported, IssueStateClosed}

// Valid reports whether the state is one the protocol defines.
func (s IssueState) Valid() bool {
	for _, known := range issueStates {
		if s == known {
			return true
		}
	}
	return false
}

// IssueActor is who caused an issue change: the engine itself, or the address
// of the session that did. The engine is not a session, so a change the engine
// makes names itself here instead of borrowing a session's name.
type IssueActor string

// ActorEngine is the engine itself, as when the PR watcher closes an issue
// because the PR that fixed it merged.
const ActorEngine IssueActor = "engine"

// String names the actor in a sentence: a session address as it is, and the
// engine in words, because "engine closed issue D-7" reads badly. The agent
// text of a system event renders a named string type through String.
func (a IssueActor) String() string {
	if a == ActorEngine {
		return "The engine"
	}
	return string(a)
}

// IssueFile is one place an issue points at. A line of zero means the whole
// file.
type IssueFile struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// IssueEvent tells the room that an issue was filed or changed. It carries
// every fact the card shows, so the card never reads the post text.
//
// A new issue is a backlog item: the event wakes nobody, root included. An
// update or a close is equally quiet and reaches the owner feed only.
type IssueEvent struct {
	Type IssueChange `json:"type"`
	// IssueID is empty at filing time: the issue's ID is the post's own ID.
	IssueID string      `json:"issueId,omitempty"`
	Number  int         `json:"number"`
	Title   string      `json:"title"`
	Files   []IssueFile `json:"files,omitempty"`
	Symbols []string    `json:"symbols,omitempty"`
	// Cost and Direction are the librarian's short note fields.
	Cost      string     `json:"cost,omitempty"`
	Direction string     `json:"direction,omitempty"`
	State     IssueState `json:"state"`
	Filer     string     `json:"filer"`
	// Actor is who caused this change: the filer for a filing, the closer for
	// a close. An automatic close names the engine, never the session whose
	// authority the engine used. It is empty when the post names nobody else.
	Actor    IssueActor   `json:"actor,omitempty"`
	Assignee string       `json:"assignee,omitempty"`
	ClosedBy *CloseOrigin `json:"closedBy,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	// Detail is the note body an agent reads: the filed note, or the evidence a
	// later note added. A close reason is one line, and the close text already
	// carries it.
	Detail string `json:"detail,omitempty"`
}

func (IssueEvent) eventFamily() EventFamily { return FamilyIssue }
func (p IssueEvent) eventType() EventType   { return EventType(p.Type) }

// eventSubject is the issue's room number, so every event of one issue folds
// into one card.
func (p IssueEvent) eventSubject() string { return "D-" + strconv.Itoa(p.Number) }

// eventDedupe keys one post per issue and change: a retried filing keeps its
// post, and a second close of the same issue posts nothing.
func (p IssueEvent) eventDedupe() string {
	return string(p.Type) + "\x00" + string(p.State)
}
