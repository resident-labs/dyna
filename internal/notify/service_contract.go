package notify

import (
	"fmt"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// servicePost checks identity and recipient structure before any post is stored.
// System integrations use their own posting identity and do not enter this path.
func (s *Service) servicePost(in PostInput, res resolved) error {
	if in.Sender.Kind != SenderSession {
		return nil
	}
	meta := in.Sender.Session.Meta
	contract := meta.ServiceContract()
	if !contract.Active() {
		return nil
	}
	if in.Kind == "alert" {
		if len(res.recipients) == 1 && ((!contract.Helper() && res.recipients[0].kind == toOwner) ||
			contract.Helper() && res.recipients[0].kind == toSession && res.recipients[0].session.ID == meta.ParentSession) {
			return nil
		}
		return subagent.ErrServiceOutput
	}
	if in.Kind != "" && in.Kind != "message" {
		return subagent.ErrServiceOutput
	}
	if in.ReplyTo != "" {
		original, err := s.Rooms.Message(res.rec.ID, in.ReplyTo)
		if err != nil {
			return fmt.Errorf("%w: %v", subagent.ErrServiceOutput, err)
		}
		if len(res.recipients) == 1 && contract.DirectMention(original) && original.SentBySession(res.recipients[0].session.ID, res.recipients[0].addr) {
			return nil
		}
		return subagent.ErrServiceOutput
	}
	for _, target := range res.recipients {
		if target.kind != toSession || !contract.CrewTarget(target.session.Meta) {
			return subagent.ErrServiceOutput
		}
	}
	return nil
}

func (s *Service) previousServiceAlert(in PostInput, res resolved, text string) (room.Message, error) {
	if in.Sender.Kind != SenderSession || !in.Sender.Session.Service || in.Kind != "alert" {
		return room.Message{}, nil
	}
	posts, err := s.Rooms.Messages(res.rec.ID)
	if err != nil {
		return room.Message{}, err
	}
	cutoff := time.Now().Add(-time.Hour).UnixMilli()
	created := in.Sender.Session.Created.UnixMilli()
	for i := len(posts) - 1; i >= 0; i-- {
		post := posts[i]
		target := res.recipients[0]
		sameTarget := len(post.To) == 1 && post.To[0] == target.addr && (target.kind == toOwner || post.SessionIDs[target.addr] == target.session.ID)
		if sameTarget && post.SessionIDs[res.from] == in.Sender.Session.ID && post.From == res.from && post.Kind == "alert" && post.Text == text && post.TS >= cutoff && post.TS >= created {
			return post, nil
		}
	}
	return room.Message{}, nil
}

// helperCompletion delivers this turn's findings to the current parent.
// An explicit findings post suppresses a duplicate completion report.
func (s *Service) helperCompletion(meta subagent.Meta, turn subagent.Turn) (*subagent.ReportPlan, error) {
	parent, err := s.sessionStore().DeliveryTarget(meta.ParentSession)
	available := err == nil && parent.RoomID == meta.RoomID && (parent.Transfer.Active() || usable(parent) == "")
	posts, err := s.Rooms.Messages(meta.RoomID)
	if err != nil {
		return nil, err
	}
	for _, post := range posts {
		if parent.ID != "" && post.From == meta.Address() && post.SessionIDs[meta.Address()] == meta.ID && post.Turn == turn.N && post.SessionIDs[parent.Address()] == parent.ID &&
			(post.Kind == "message" || post.Kind == "alert") {
			return nil, nil
		}
	}
	if !available {
		return s.prepareCompletion(meta.RoomID, meta.Address(), nil, "alert",
			"Service helper finished, but its parent is unavailable. Full output: dyna subagent output "+meta.Address(), turn.N, true)
	}
	if resultPreview(turn.Result, 1) != "" {
		text, err := s.boundSessionReport(meta.ID, turn.N, CompletionText(meta.Address(), turn.N, "completed", turn.Result, ""))
		if err != nil {
			return nil, err
		}
		return s.prepareCompletion(meta.RoomID, meta.Address(), []string{parent.ID}, "completed", text, turn.N, false)
	}
	return s.prepareCompletion(meta.RoomID, meta.Address(), []string{parent.ID}, "alert",
		"Service turn produced no artifact: helper finished without posting findings to its parent.", turn.N, false)
}
