package engine

import (
	"context"
	"errors"
	"log"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/websvc"
)

// post stores and delivers a post that a CLI process sent (requirement 2).
// Its sender comes from the caller, not from the request (requirement 3).
func (s services) post(ctx context.Context, req notify.PostRequest) (notify.Result, error) {
	caller, local := websvc.CallerFromContext(ctx)
	snd, err := postSender(caller, local, authority.Of(ctx), req.Claim, enforceCallers, log.Printf, s.rooms.Notify.SenderOf)
	if err != nil {
		return notify.Result{}, err
	}
	res, err := s.rooms.Notify.Post(ctx, req.Input(snd))
	// A stored post was sent. Its failed deliveries are in the result, so a
	// caller reports them instead of sending the post a second time.
	if errors.Is(err, notify.ErrDelivery) && res.Message.ID != "" {
		return res, nil
	}
	return res, err
}

// postSender uses the verified caller identity. Only room routing comes
// from the claim; root-mode CLI processes carry an owner claim.
func postSender(caller websvc.Caller, local bool, actor authority.Actor, claim notify.Claim, _ bool, _ func(string, ...any), senderOf func(notify.Claim) (notify.Sender, error)) (notify.Sender, error) {
	as, ok := verified(caller, local, actor)
	if !ok {
		return notify.Sender{}, errors.New("the engine could not verify who is posting")
	}
	as.Room = claim.Room
	// A run's coordinator posts only through its script's post().
	if as.Run != "" {
		return notify.Sender{}, errors.New("a workflow run's coordinator posts only through its script's post()")
	}
	return senderOf(as)
}

// verified is the claim that the resolved caller proves, if it proves one.
// A run's coordinator proves its run, with agent 0.
func verified(caller websvc.Caller, local bool, actor authority.Actor) (notify.Claim, bool) {
	switch {
	case !local || caller.Kind == websvc.Owner:
		return notify.Claim{}, true
	case caller.Kind == websvc.Session && actor.Session == caller.Session:
		return notify.Claim{Session: caller.Session}, true
	case caller.Kind == websvc.Worker && actor.Role == authority.Worker && actor.Run == caller.Run:
		return notify.Claim{Run: actor.Run, Agent: actor.Agent}, true
	}
	return notify.Claim{}, false
}

// claimed names who c says posts, without its room.
