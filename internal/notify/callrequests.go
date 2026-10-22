package notify

import (
	"fmt"
	"path/filepath"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (s *Service) CallRequestStore() *callrequests.Store {
	return &callrequests.Store{Root: filepath.Dir(s.sessionStore().Root)}
}

// CallRoom checks the stored sender role before resolving the sender's room.
func (s *Service) CallRoom(snd Sender, ref string) (room.Record, error) {
	if snd.Kind == SenderSession {
		meta, err := s.sessionStore().ReadMeta(snd.Session.ID)
		if err != nil {
			return room.Record{}, err
		}
		if meta.ServiceContract().Active() {
			return room.Record{}, subagent.ErrServiceOutput
		}
		snd.Session.Role = meta.Role
		snd.Room = meta.Room
	}
	if senderActor(snd).Role != authority.Root {
		return room.Record{}, fmt.Errorf("call is root only; use dyna post @parent to request a call")
	}
	return s.QuestionRoom(snd, ref)
}

func (s *Service) RequestCall(snd Sender, ref, reason string) (callrequests.Request, error) {
	rec, err := s.CallRoom(snd, ref)
	if err != nil {
		return callrequests.Request{}, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return callrequests.Request{}, fmt.Errorf("--reason is required: say why the owner should join the call")
	}
	req, _, err := s.CallRequestStore().Ask(rec.ID, reason, callIdentity(snd))
	return req, err
}

func (s *Service) WithdrawCall(snd Sender, ref, id string) error {
	rec, err := s.CallRoom(snd, ref)
	if err != nil {
		return err
	}
	_, _, err = s.CallRequestStore().Close(rec.ID, id, "withdrawn", callIdentity(snd))
	return err
}

func callIdentity(snd Sender) string {
	if snd.Kind == SenderSession {
		return snd.Session.ID
	}
	return "root"
}
