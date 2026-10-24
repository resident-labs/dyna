package notify

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// deliverRoot hands a stored post to the room's internal root while it holds
// the root seat. The internal root's input is a post the seat's delivery rows
// mark as its own (room.Delivery.RootInput), including an explicitly marked
// root-seat handoff. The owner's request (room.OwnerRequest), or a handoff,
// starts the internal root when none is open. It reports false when the post
// is not the internal root's input.
func (s *Service) deliverRoot(ctx context.Context, msg room.Message, rec room.Record) (Delivery, bool) {
	addr := rec.Name + ":" + room.RootAgent
	// direct names the seat by its own address. A post stored before @parent
	// resolved to the room's address reaches the seat this way, and so does a
	// check-in, which the engine posts to the parent's address.
	direct := msg.Addressed(addr)
	seat, ok, err := s.Rooms.SeatDelivery(rec.ID, msg.ID)
	if err != nil {
		return Delivery{To: addr, Delivery: "failed", Error: err.Error()}, true
	}
	if !ok || !seat.RootInput() || mode.InWorkflow() {
		return Delivery{}, false
	}
	cur, err := s.Rooms.Resolve(rec.ID)
	if err != nil {
		return Delivery{To: addr, Delivery: "failed", Error: err.Error()}, true
	}
	if cur.Seat.External {
		return Delivery{}, false
	}
	info, ok, err := s.Sessions.RootSession(rec.ID)
	if err != nil {
		return Delivery{To: addr, Delivery: "failed", Error: err.Error()}, true
	}
	if ok && usable(info) == "" {
		return s.deliver(msg, recipient{kind: toSession, addr: addr, rec: cur, session: info}), true
	}
	// A post the seat's row marks as a wake asks root to act, so a session's post
	// to the seat starts the root when none is open, as an address to the seat
	// still does (the old seat address, which a check-in also names). The owner's
	// request always does; the engine's own events wait for the root that reads
	// them, as they did.
	sessionPost := msg.SessionIDs[msg.From] != ""
	if !room.OwnerRequest(msg, rec.Name) && !direct && !(sessionPost && seat.Mode == room.DeliveryWake) {
		return Delivery{}, false
	}
	if ok {
		// An open root that cannot take posts never ran a turn to the end.
		// Replacing it is the engine's own maintenance, whoever posted: a
		// session that posts to root may not close root itself.
		if _, err := s.Sessions.CloseForEngine(ctx, info.ID, "replaced a root that could not take posts, after a direct post from "+msg.From, subagent.CloseOptions{}); err != nil {
			return Delivery{To: addr, Delivery: "failed", Error: err.Error()}, true
		}
	}
	started, fresh, err := s.Sessions.StartRoot(cur, "", msg.ID)
	if err != nil {
		return Delivery{To: addr, Delivery: "failed", Error: err.Error()}, true
	}
	if !fresh {
		return s.deliver(msg, recipient{kind: toSession, addr: addr, rec: cur, session: started}), true
	}
	return Delivery{To: addr, Delivery: "root-started", Turn: 1}, true
}

// SeatAttach is what attaching an external orchestrator did.
type SeatAttach struct {
	Seat    room.Seat `json:"seat"`
	Changed bool      `json:"changed"`
	// Note is the internal root's handoff, or its transcript fallback.
	Note string `json:"note,omitempty"`
	// StoppedTurn is the internal root's turn that the attach stopped, or 0.
	StoppedTurn int `json:"stoppedTurn,omitempty"`
}

// harnessName is an empty or one-word harness label.
var harnessName = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)

// AttachSeat gives the room's root seat to an external orchestrator. An open
// internal root first gets up to 30 seconds to write a handoff, which joins
// the seat note. Then its running turn stops: one seat means one acting root.
// The caller checks "root attach" first.
func (s *Service) AttachSeat(ctx context.Context, rec room.Record, harness string) (SeatAttach, error) {
	name := strings.ToLower(strings.TrimSpace(harness))
	if !harnessName.MatchString(name) {
		return SeatAttach{}, fmt.Errorf("--harness takes one word, such as claude, codex, opencode, pi, grok, muse or antigravity; got %q", harness)
	}
	var out SeatAttach
	if !rec.Seat.External {
		note, err := s.Sessions.RootSeatHandoff(ctx, rec.ID)
		if err != nil {
			return SeatAttach{}, fmt.Errorf("collect the internal root's handoff: %w", err)
		}
		out.Note = note
	}
	label := "An external orchestrator took the root seat"
	if name != "" {
		label = name + " (external) took the root seat"
	}
	note := out.Note
	var attachments []room.Attachment
	// The post text is the label, a blank line, then the note.
	if len(label)+2+len(note) > room.MaxMessageBytes {
		file, err := s.Rooms.PutAttachment(rec.ID, "root-handoff.txt", []byte(out.Note))
		if err != nil {
			return SeatAttach{}, fmt.Errorf("save the full root handoff: %w", err)
		}
		attachments = append(attachments, file)
		const tail = "\n\n[Full handoff attached as root-handoff.txt.]"
		note = room.TruncateUTF8(note, max(0, room.MaxMessageBytes-len(label)-2-len(tail))) + tail
	}
	seat, changed, err := s.Rooms.SetSeat(ctx, rec.ID, room.Seat{External: true, Harness: name}, nil)
	if err != nil {
		return SeatAttach{}, err
	}
	out.Seat, out.Changed = seat, changed
	if !changed {
		return out, nil
	}
	if _, err := s.Rooms.PostEventInput(room.PostEventInput{
		RoomID:      rec.ID,
		Event:       room.SessionEvent{Type: room.SessionSeat, Session: rec.Name, Seat: label, Detail: note},
		FreshID:     true,
		To:          []string{rec.Name, rec.Name + ":" + room.RootAgent},
		Attachments: attachments,
	}); err != nil {
		return out, err
	}
	info, ok, err := s.Sessions.RootSession(rec.ID)
	if err != nil || !ok || info.Status != subagent.StatusRunning && info.Status != subagent.StatusQueued {
		return out, err
	}
	turn, err := s.Sessions.Stop(ctx, info.ID)
	if err != nil {
		return out, fmt.Errorf("the seat is yours, but the internal root's turn did not stop: %w", err)
	}
	out.StoppedTurn = turn.N
	return out, nil
}

// ReturnSeat gives the room's root seat back to the internal root. Posts that
// the external orchestrator handled never reach the internal root. An open
// internal root gets a seat note as its next input, without a wake. A handoff
// joins the note and wakes the internal root, starting one if none is open.
// The caller checks "root detach" first.
func (s *Service) ReturnSeat(ctx context.Context, rec room.Record, handoff string) (room.Seat, bool, []Delivery, error) {
	before := rec.Seat
	seat, changed, _, err := s.Sessions.ReturnRootSeat(ctx, rec.ID)
	if err != nil {
		return seat, changed, nil, err
	}
	if !changed && handoff == "" {
		return seat, changed, nil, nil
	}
	parts := []string{"Internal root took back the seat"}
	if changed {
		parts = append(parts, "[ROOT SEAT] The external orchestrator held the root seat from "+before.Since.Local().Format(time.RFC3339)+" until "+seat.Since.Local().Format(time.RFC3339)+". When it attached, Dyna stopped your turn if one was running. Read `dyna room log "+rec.ID+" --all` for what happened meanwhile.")
	}
	var attachments []room.Attachment
	label := parts[0]
	note := strings.Join(parts[1:], "\n\n")
	if handoff != "" {
		review := subagent.Review("", "The external orchestrator returned its open work in the written handoff", fmt.Sprintf("`dyna room log %s --all`", rec.ID))
		prefix := "[ROOT SEAT HANDOFF]\n" + review + "\nWritten handoff:\n"
		note = strings.Join(parts[1:], "\n\n")
		if note != "" {
			prefix = note + "\n\n" + prefix
		}
		note = prefix + handoff
		if len(label)+2+len(note) > room.MaxMessageBytes {
			const tail = "\n\n[Written handoff continues in root-handoff-rest.txt.]"
			inline := room.TruncateUTF8(handoff, max(0, room.MaxMessageBytes-len(label)-2-len(prefix)-len(tail)))
			file, err := s.Rooms.PutAttachment(rec.ID, "root-handoff-rest.txt", []byte(handoff[len(inline):]))
			if err != nil {
				return seat, changed, nil, err
			}
			attachments = append(attachments, file)
			note = prefix + inline + tail
		}
	}
	addr := rec.Name + ":" + room.RootAgent
	event := room.SessionEvent{Type: room.SessionSeat, Session: rec.Name, Seat: label, Detail: note}
	msg, err := s.Rooms.PostEventInput(room.PostEventInput{
		RoomID:      rec.ID,
		Event:       event,
		FreshID:     true,
		To:          []string{rec.Name, addr},
		Via:         []string{"@root"},
		NeedsRoot:   handoff != "",
		Attachments: attachments,
	})
	if err != nil || handoff == "" {
		return seat, changed, nil, err
	}
	d, _ := s.deliverRoot(ctx, msg, rec)
	return seat, changed, []Delivery{d}, DeliveryError(Result{Message: msg, Deliveries: []Delivery{d}})
}
