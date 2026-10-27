package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/voice"
	"dyna-agent/internal/voice/agentwire"
	"dyna-agent/internal/websvc"
)

// agentVoice runs the live voice sessions of agents (dyna voice session). It
// identifies the caller like any engine call, and tells the caller's room when
// a session starts and ends, so the owner sees who spends the voice quota.
type agentVoice struct {
	sessions sessionRunner
	// actor is what the local caller acts for.
	actor func(websvc.Caller) (authority.Actor, error)
	// address is the readable address of the actor, such as room:name.
	address func(authority.Actor) string
	// post tells a room about a session.
	post func(roomID string, ev room.VoiceSessionEvent) error
	// newID makes the ID that pairs a session's start and end.
	newID func() string
}

// sessionRunner runs one live voice session.
type sessionRunner interface {
	Run(ctx context.Context, req agentwire.SessionRequest, io voice.SessionIO) (voice.SessionEnd, error)
}

// Session runs one session for caller until it ends.
func (h *agentVoice) Session(ctx context.Context, caller websvc.Caller, stream websvc.AgentVoiceStream) error {
	actor, err := h.actor(caller)
	if err != nil {
		return err
	}
	switch actor.Role {
	case authority.Owner, authority.Root, authority.Session, authority.Worker:
	default:
		return errors.New("voice session needs a verified caller")
	}
	first, err := stream.Read()
	if err != nil {
		return err
	}
	if first.Type != agentwire.InStart || first.Request == nil {
		return errors.New("the first line of a voice session must be its start")
	}
	in := make(chan agentwire.InMessage)
	go func() {
		defer close(in)
		for {
			m, err := stream.Read()
			if err != nil || m.Type == agentwire.InEnd {
				return
			}
			select {
			case in <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	id, who := h.newID(), h.address(actor)
	tell := func(ev room.VoiceSessionEvent) {
		// An owner outside any room has no room to tell.
		if actor.Room == "" {
			return
		}
		ev.Voice, ev.Session = id, who
		if err := h.post(actor.Room, ev); err != nil {
			log.Printf("voice session notice for %s: %v", who, err)
		}
	}
	end, err := h.sessions.Run(ctx, *first.Request, voice.SessionIO{
		In:      in,
		Out:     stream.Write,
		Started: func(provider string) { tell(room.VoiceSessionEvent{Type: room.VoiceStarted, Provider: provider}) },
	})
	if err != nil {
		return err
	}
	tell(room.VoiceSessionEvent{Type: room.VoiceEnded, Provider: end.Provider, Seconds: int(end.Elapsed.Seconds()), Reason: roomEndReason(end.Reason)})
	return nil
}

// roomEndReason is the room's name for why a session ended.
func roomEndReason(r agentwire.EndReason) room.VoiceEndReason {
	switch r {
	case agentwire.EndInputClosed:
		return room.VoiceEndInput
	case agentwire.EndTimeLimit:
		return room.VoiceEndTimeLimit
	case agentwire.EndIdle:
		return room.VoiceEndIdle
	case agentwire.EndProviderClosed:
		return room.VoiceEndProvider
	case agentwire.EndCancelled:
		return room.VoiceEndCancelled
	}
	return room.VoiceEndError
}

// newVoiceID is a random ID for one voice session.
func newVoiceID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// agentVoice returns the live voice session host for rooms, which acts for
// each caller as the engine's other calls do.
func (s *socketCallers) agentVoice(rooms *roomsvc.Service) *agentVoice {
	return &agentVoice{
		sessions: voice.NewSessions(),
		actor:    s.actor,
		address: func(a authority.Actor) string {
			if a.Session != "" {
				if meta, err := s.sessions.ReadMeta(a.Session); err == nil {
					return meta.Address()
				}
			}
			if a.Run != "" {
				return "workflow " + a.Run
			}
			return "owner"
		},
		post:  postVoiceSession(rooms),
		newID: newVoiceID,
	}
}

// postVoiceSession posts a session notice to the room's own address. The
// event wakes nobody.
func postVoiceSession(rooms *roomsvc.Service) func(string, room.VoiceSessionEvent) error {
	return func(roomID string, ev room.VoiceSessionEvent) error {
		rec, err := rooms.Notify.Rooms.Resolve(roomID)
		if err != nil {
			return err
		}
		_, err = rooms.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: roomID, Event: ev, Producer: room.ProducerEngine, To: []string{rec.Name}})
		return err
	}
}
