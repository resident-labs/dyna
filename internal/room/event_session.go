package room

import (
	"strconv"
	"strings"
)

// SessionChange is what a session note reports about its subject.
type SessionChange string

// Session changes.
const (
	// SessionRenamed is a session that took a new name.
	SessionRenamed SessionChange = "renamed"
	// SessionPromoted is a session that moved to the top level.
	SessionPromoted SessionChange = "promoted"
	// SessionAdopted is a session that moved under another session.
	SessionAdopted SessionChange = "adopted"
	// SessionAccount is a turn that moved to another account or profile.
	SessionAccount SessionChange = "account"
	// SessionInstructions is a standing-instruction change that a session
	// must apply from its next turn.
	SessionInstructions SessionChange = "instructions"
	// SessionSeat is a change of who holds the room's root seat.
	SessionSeat SessionChange = "seat"
	// SessionSpawned is a session the owner started from the composer.
	SessionSpawned SessionChange = "spawned"
	// SessionTransfer asks a session for the handoff a model transfer needs.
	SessionTransfer SessionChange = "transfer-request"
	// SessionComputer reminds a session that its computer is still open.
	SessionComputer SessionChange = "computer-reminder"
)

// sessionChanges lists every session change the web needs as a union type.
var sessionChanges = []SessionChange{
	SessionRenamed, SessionPromoted, SessionAdopted, SessionAccount,
	SessionInstructions, SessionSeat, SessionSpawned, SessionTransfer, SessionComputer,
}

// AccountReason is why a turn moved to another account or profile.
type AccountReason string

// Account reasons.
const (
	// AccountUsageLimit is an account that reached its provider limit.
	AccountUsageLimit AccountReason = "usage-limit"
	// AccountFallback is a profile that reached its provider limit, whose
	// session moved to the profile's fallback.
	AccountFallback AccountReason = "fallback"
)

// accountReasons lists every account reason the web needs as a union type.
var accountReasons = []AccountReason{AccountUsageLimit, AccountFallback}

// InstructionAction is what happened to one section of the standing
// instructions. It picks the wording of the note's row.
type InstructionAction string

// Instruction actions.
const (
	// InstructionItems is a section whose tracked lines changed.
	InstructionItems InstructionAction = "items"
	// InstructionBody is a section whose whole text changed.
	InstructionBody InstructionAction = "body"
	// InstructionRemoved is a section that is gone.
	InstructionRemoved InstructionAction = "removed"
)

// instructionActions lists every instruction action the web needs as a union.
var instructionActions = []InstructionAction{InstructionItems, InstructionBody, InstructionRemoved}

// InstructionChange is one section that a standing-instruction note names.
type InstructionChange struct {
	Name   string            `json:"name"`
	Action InstructionAction `json:"action"`
	// Added and Removed are the tracked lines the section gained and lost.
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Body is the section's new text, for a whole-text change.
	Body string `json:"body,omitempty"`
}

// SessionEvent is one note about a session: a rename, a move, an account
// change, a standing-instruction change, a root-seat change or a spawn. Every
// field is typed, so the note's row and the agent text carry the same facts.
//
// Session is the address of the session the note is about. A seat change is
// about the room's root seat, so it names the room.
type SessionEvent struct {
	Type    SessionChange `json:"type"`
	Session string        `json:"session"`
	// Name and OldName are the session's name before and after a rename.
	Name    string `json:"name,omitempty"`
	OldName string `json:"oldName,omitempty"`
	// From and To are the parents a move left and joined, or the account and
	// profile an account change left and joined. "top level" means no parent.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Reason is why an account change happened.
	Reason AccountReason `json:"reason,omitempty"`
	// Profile is the profile a spawned session runs on, or a transfer's target.
	Profile string `json:"profile,omitempty"`
	// Transfer is the transfer ID a handoff request names, so the card can
	// point at the transfer that asked for it.
	Transfer string `json:"transfer,omitempty"`
	// Dir is the directory a spawned session works in.
	Dir string `json:"dir,omitempty"`
	// Seat is the label of a root-seat change.
	Seat string `json:"seat,omitempty"`
	// Apps are the titles of the apps open on a computer that a reminder names.
	Apps []string `json:"apps,omitempty"`
	// Sections lists the instruction sections that changed.
	Sections []InstructionChange `json:"sections,omitempty"`
	// Occurrence counts the notes about one kind of change, so a change that
	// reverts to an earlier state is still a new note. A poster without an
	// occurrence of its own asks for a fresh post ID instead.
	Occurrence int `json:"occurrence,omitempty"`
	// Detail is the evidence the agent reads: the instruction lines, the root
	// handoff, the clipped task of a spawn, or the transfer note.
	Detail string `json:"detail,omitempty"`
}

func (SessionEvent) eventFamily() EventFamily { return FamilySession }
func (s SessionEvent) eventType() EventType   { return EventType(s.Type) }

// eventSubject keeps every note of one session in that session's rows.
func (s SessionEvent) eventSubject() string { return s.Session }

func (s SessionEvent) eventThread() string {
	if s.Type == SessionInstructions && s.Session != "" {
		return "instructions:" + s.Session
	}
	return ""
}

// eventDedupe keys a note by the facts of its change, so a retried note posts
// once and a second change posts again.
func (s SessionEvent) eventDedupe() string {
	switch s.Type {
	case SessionRenamed:
		return s.OldName + "\x00" + s.Name
	case SessionPromoted, SessionAdopted:
		return s.From + "\x00" + s.To
	case SessionAccount:
		return s.From + "\x00" + s.To + "\x00" + string(s.Reason)
	case SessionInstructions:
		// The occurrence comes first, so a change that reverts to an earlier
		// state is still a new note while a retry of one change keeps its post.
		// The sections and the body follow it, because two different changes
		// to one section are two notes.
		lines := make([]string, len(s.Sections)+1)
		lines[0] = strconv.Itoa(s.Occurrence)
		for i, change := range s.Sections {
			lines[i+1] = string(change.Action) + " " + change.Name + "\x00" + strings.Join(change.Added, "\x01") + "\x00" + strings.Join(change.Removed, "\x01") + "\x02" + change.Body
		}
		return strings.Join(lines, "\x00")
	case SessionSeat:
		return s.Seat
	case SessionTransfer:
		return s.Transfer
	}
	return s.Profile
}

// InstructionBatchEvent summarizes one shared instruction change for the
// owner. The per-session SessionEvent remains the agent's own input in its DM;
// this quiet room event replaces the burst of owner-facing session rows.
type InstructionBatchEvent struct {
	Sections []InstructionChange `json:"sections"`
	Sessions []string            `json:"sessions"`
	// Notes names the per-session posts this summary replaces in the owner
	// feed. Root context skips those source posts and reads this row once.
	Notes []string `json:"notes"`
}

func (InstructionBatchEvent) eventFamily() EventFamily { return FamilySession }
func (InstructionBatchEvent) eventType() EventType     { return TypeInstructionBatch }
func (b InstructionBatchEvent) eventSubject() string   { return strings.Join(b.Sessions, "\x00") }

// eventDedupe is a fallback for callers without a source-note ID. The manager
// supplies an ID derived from the source notes, so a later identical edit is a
// new batch while a retry keeps its post.
func (b InstructionBatchEvent) eventDedupe() string {
	lines := make([]string, len(b.Sections))
	for i, change := range b.Sections {
		lines[i] = string(change.Action) + " " + change.Name + "\x00" + strings.Join(change.Added, "\x01") + "\x00" + strings.Join(change.Removed, "\x01") + "\x02" + change.Body
	}
	return strings.Join(lines, "\x00")
}
