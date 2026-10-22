package notify

import (
	"context"
	handofftext "dyna-agent/internal/handoff"
	"fmt"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// deliverRoot hands a stored post to the room's internal root while it holds
// the root seat. The internal root's input is a post to the owner
// (room.WakesRoot), including an explicitly marked root-seat handoff.
// The owner's request (room.OwnerRequest), or a handoff, starts the internal root when none is
// open. It reports false when the post is not the internal root's input.
func (s *Service) deliverRoot(ctx context.Context, msg room.Message, rec room.Record) (Delivery, bool) {
	addr := rec.Name + ":" + room.RootAgent
	direct := msg.Addressed(addr)
	if !room.RootInput(msg, rec.Name) || mode.InWorkflow() {
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
	if !room.OwnerRequest(msg, rec.Name) && !direct {
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
	text := strings.TrimSpace(label + "\n\n" + out.Note)
	var attachments []room.Attachment
	if len(text) > room.MaxMessageBytes {
		file, err := s.Rooms.PutAttachment(rec.ID, "root-handoff.txt", []byte(out.Note))
		if err != nil {
			return SeatAttach{}, fmt.Errorf("save the full root handoff: %w", err)
		}
		attachments = append(attachments, file)
		const tail = "\n\n[Full handoff attached as root-handoff.txt.]"
		text = room.TruncateUTF8(text, room.MaxMessageBytes-len(tail)) + tail
	}
	seat, changed, err := s.Rooms.SetSeat(ctx, rec.ID, room.Seat{External: true, Harness: name}, nil)
	if err != nil {
		return SeatAttach{}, err
	}
	out.Seat, out.Changed = seat, changed
	if !changed {
		return out, nil
	}
	if _, err := s.Rooms.Post(rec.ID, room.PostInput{From: "dyna", Kind: "seat", To: []string{rec.Name, rec.Name + ":" + room.RootAgent}, Text: text, Attachments: attachments}); err != nil {
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
	text := strings.Join(parts, "\n\n")
	if handoff != "" {
		review := handofftext.Review("", "The external orchestrator returned its open work in the written handoff", fmt.Sprintf("`dyna room log %s --all`", rec.ID))
		prefix := text + "\n\n[ROOT SEAT HANDOFF]\n" + review + "\nWritten handoff:\n"
		text = prefix + handoff
		if len(text) > room.MaxMessageBytes {
			const tail = "\n\n[Written handoff continues in root-handoff-rest.txt.]"
			inline := room.TruncateUTF8(handoff, max(0, room.MaxMessageBytes-len(prefix)-len(tail)))
			file, err := s.Rooms.PutAttachment(rec.ID, "root-handoff-rest.txt", []byte(handoff[len(inline):]))
			if err != nil {
				return seat, changed, nil, err
			}
			attachments = append(attachments, file)
			text = prefix + inline + tail
		}
	}
	addr := rec.Name + ":" + room.RootAgent
	msg, err := s.Rooms.Post(rec.ID, room.PostInput{Wake: handoff != "", NeedsRoot: handoff != "", From: "dyna", Kind: "seat", To: []string{rec.Name, addr}, Via: []string{"@root"}, Text: text, Attachments: attachments})
	if err != nil || handoff == "" {
		return seat, changed, nil, err
	}
	d, _ := s.deliverRoot(ctx, msg, rec)
	return seat, changed, []Delivery{d}, DeliveryError(Result{Message: msg, Deliveries: []Delivery{d}})
}
