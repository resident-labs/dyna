package room

import (
	"context"
	"strings"
	"time"
)

// Seat says who holds the room's root seat. The zero value is the internal
// root session; External is an attached external orchestrator, and Harness
// names its harness (such as "claude"), empty if it gave none. Since is when
// the holder last changed, zero if it never did.
type Seat struct {
	External bool      `json:"external,omitempty"`
	Harness  string    `json:"harness,omitempty"`
	Since    time.Time `json:"since,omitzero"`
}

// SetSeat gives the seat to holder: an external orchestrator, or the internal
// root for the zero Seat. holder.Since is ignored. It reports whether the
// holder changed; an unchanged seat keeps its Since. The change is serialized with room post append. When the holder
// changes, atEnd (if set) runs first under the same lock with the room log's
// end offset, so no post lands between it and the new seat. Callers take
// session locks before calling, as WithRoomSnapshot requires.
func (s *Store) SetSeat(ctx context.Context, ref string, holder Seat, atEnd func(logEnd int64) error) (Seat, bool, error) {
	rec, err := s.Resolve(ref)
	if err != nil {
		return Seat{}, false, err
	}
	var out Seat
	var changed bool
	err = s.WithRoomSnapshot(ctx, rec.ID, func(rec Record) error {
		if rec.Seat.External == holder.External && rec.Seat.Harness == holder.Harness {
			out = rec.Seat
			return nil
		}
		if atEnd != nil {
			end, err := s.FileSize(rec.ID)
			if err != nil {
				return err
			}
			if err = atEnd(end); err != nil {
				return err
			}
		}
		seat := Seat{External: holder.External, Harness: holder.Harness, Since: s.now()}
		if err := s.writeSeat(rec.ID, seat); err != nil {
			return err
		}
		out, changed = seat, true
		return nil
	})
	return out, changed, err
}

// VoiceAgent is the address of Voice, the owner's voice call. It is not a
// session: its posts record the call, and it never takes a turn.
const VoiceAgent = "voice-call"

// SourceVoice marks the posts of the owner's voice call.
const SourceVoice = "voice"

// OwnerRequest reports whether msg is the owner's own request: a post the
// human typed, or one Voice relays from the owner's speech. Such a post
// starts the internal root and owes the owner its report.
func OwnerRequest(msg Message, roomName string) bool {
	return msg.From == roomName && msg.Human || msg.From == roomName+":"+VoiceAgent
}

// RootAgent is the internal root session's name. It is a reserved mention
// name, so no other session can take it, and @root names the seat, not it.
const RootAgent = "root"

// RootSeatTarget reports whether m was posted to the root seat: it is
// addressed to the room, and its mentions name the seat. Both "@root" and
// "@<room>:root" resolve to the room's own address, so the seat is in To as
// the room name rather than as an agent address.
func (m Message) RootSeatTarget(roomName string) bool {
	if !m.Addressed(roomName) {
		return false
	}
	for _, tok := range m.Via {
		switch strings.TrimPrefix(tok, "@") {
		case RootAgent, roomName + ":" + RootAgent:
			return true
		}
	}
	return false
}

// WakesRoot reports whether a post requires the root seat to act. Worker
// copies remain quiet unless marked. Human requests, Voice, alerts, failed
// reports and root-owned shell/workflow results keep their wake behavior.
func WakesRoot(msg Message, roomName string) bool {
	if !msg.Addressed(roomName) && !msg.Addressed(roomName+":"+RootAgent) || msg.From == roomName+":"+RootAgent {
		return false
	}
	// A registered event follows its spec, so its family decides how it
	// reaches the root seat: a quiet one never wakes root, one that wakes its
	// target wakes root only when the post needs root, and an alert wakes it
	// now.
	if spec, ok := eventSpecFor(msg.Kind, msg.Notice.Kind); ok {
		switch spec.Wake {
		case WakeNever:
			return false
		case WakeRoot:
			return true
		}
		return msg.NeedsRoot
	}
	switch msg.Kind {
	case "", "message":
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice
	// TODO: legacy/shim remove later — a post stored before its family was
	// typed still reads by its old kind. Remove when retained history ages out.
	case "notice":
		return msg.NeedsRoot || msg.Human || OwnerRequest(msg, roomName) || msg.Source == SourceVoice
	case "alert", "service-alert":
		return true
	case "seat", "pr", "merge":
		return msg.NeedsRoot
	case "completed":
		return msg.OwnerReport && (strings.HasPrefix(msg.From, "shell:") || strings.HasPrefix(msg.From, "run:"))
	case "failed", "stopped":
		return msg.OwnerReport
	}
	return false
}

// RootInput includes direct engine notes and owner replies as well as root
// action requests. Passive engine notes are drained with the next real input;
// their Wake field stays false. Worker copies require explicit action intent.
// RootContext builds the root context feed on top of it.
func RootInput(msg Message, roomName string) bool {
	// A check-in is input for the parent it names, whoever posted it: the
	// engine now, and "system:parent-checkin" for a post stored before the
	// typed events. The family check covers both.
	return WakesRoot(msg, roomName) || msg.Addressed(roomName+":"+RootAgent) && (msg.From == roomName || msg.From == "dyna" || msg.Kind == string(FamilyCheckin))
}
