package engine

import (
	"context"
	"errors"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/websvc"
)

func (s services) requestCall(ctx context.Context, room, dir, reason string) (callrequests.Request, error) {
	snd, ref, err := s.callTarget(ctx, room, dir)
	if err != nil {
		return callrequests.Request{}, err
	}
	return s.rooms.Notify.RequestCall(snd, ref, reason)
}

func (s services) withdrawCall(ctx context.Context, room, dir, id string) error {
	snd, ref, err := s.callTarget(ctx, room, dir)
	if err != nil {
		return err
	}
	return s.rooms.Notify.WithdrawCall(snd, ref, id)
}

// callSender takes identity only from the engine's verified caller. The
// existing call service checks the stored root role and room before writing.
func (s services) callSender(ctx context.Context) (notify.Sender, error) {
	caller, local := websvc.CallerFromContext(ctx)
	return s.callSenderFor(caller, local, authority.Of(ctx))
}

func (s services) callSenderFor(caller websvc.Caller, local bool, actor authority.Actor) (notify.Sender, error) {
	as, ok, err := recordedCaller(caller, local, actor, enforceCallers, log.Printf, "requesting a call")
	if err != nil {
		return notify.Sender{}, err
	}
	if !ok {
		return notify.Sender{}, errors.New("call requires a verified root caller")
	}
	if as.Run != "" {
		return notify.Sender{}, errors.New("call is root only; use dyna post @parent to request a call")
	}
	return s.rooms.Notify.SenderOf(as)
}

func (s services) callTarget(ctx context.Context, ref, dir string) (notify.Sender, string, error) {
	snd, err := s.callSender(ctx)
	if err != nil {
		return snd, "", err
	}
	if ref == "" && snd.Room == "" {
		if dir == "" {
			return snd, "", errors.New("a call request needs a room or the caller's working directory")
		}
		rec, err := s.rooms.Notify.Rooms.EnsureRepoRoom(dir, "root")
		if errors.Is(err, room.ErrNotRepo) {
			err = room.ErrOutsideRepo
		}
		if err != nil {
			return snd, "", err
		}
		ref = rec.ID
	}
	return snd, ref, nil
}
