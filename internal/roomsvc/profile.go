package roomsvc

import (
	"context"
	"errors"
	"strings"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/workeroutput"
)

// Profile is a member's card: identity and its newest posts. The web card
// shows the goals the member holds from the room's goal list.
type Profile struct {
	Member Member             `json:"member"`
	Parent string             `json:"parent,omitempty"` // the parent's name; empty for a top-level session
	Dir    string             `json:"dir"`
	Usage  workeroutput.Usage `json:"usage"`
	Posts  []Post             `json:"posts"` // the member's newest posts, oldest first
}

// Profile reads a session's card with at most posts of its newest posts.
func (s *Service) Profile(ref string, posts int) (Profile, error) {
	info, err := s.sessions().ResolveSnapshot(ref)
	if err != nil {
		return Profile{}, err
	}
	out := Profile{Member: s.member(info, 0, 0), Dir: info.Dir, Usage: info.Usage}
	book := s.avatars(info.RoomID)
	out.Member.Emoji = book.byID[info.ID]
	if info.ParentSession != "" {
		if parent, err := s.sessions().Inspect(info.ParentSession); err == nil {
			out.Parent = parent.EffectiveName()
		}
	}
	rec, err := s.rooms().Resolve(info.RoomID)
	if err != nil {
		return out, err
	}
	msgs, err := s.rooms().Messages(rec.ID)
	if err != nil {
		return out, err
	}
	reads, err := s.rooms().OwnerReadIDs(rec.ID)
	if err != nil {
		return out, err
	}
	addr := info.Address()
	for i := len(msgs) - 1; i >= 0 && len(out.Posts) < posts; i-- {
		if msgs[i].From == addr {
			out.Posts = append(out.Posts, s.expand(decorate(msgs[i], rec.Name, reads, book), book))
		}
	}
	for i, j := 0, len(out.Posts)-1; i < j; i, j = i+1, j-1 {
		out.Posts[i], out.Posts[j] = out.Posts[j], out.Posts[i]
	}
	return out, nil
}

// SendToSession posts text to one session as the human owner, from its
// session view: it steers a running turn or starts the next one.
func (s *Service) SendToSession(ctx context.Context, ref, text, forwardOf string, atts ...room.Attachment) (PostResult, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(atts) == 0 && forwardOf == "" {
		return PostResult{}, errors.New("type a message first")
	}
	info, err := s.sessions().Resolve(ref)
	if err != nil {
		return PostResult{}, err
	}
	res, err := s.Notify.Post(ctx, notify.PostInput{
		Sender:   notify.Sender{Kind: notify.SenderOwner, Room: info.Room, Human: true},
		Mentions: []string{"@" + info.Address()}, Text: text, Attachments: atts, ForwardOf: forwardOf,
	})
	if res.Message.ID != "" {
		_ = s.rooms().MarkOwnerRead(res.Message.RoomID, res.Message.ID)
	}
	return PostResult{Message: res.Message, Deliveries: res.Deliveries, Failed: res.Failed()}, err
}
