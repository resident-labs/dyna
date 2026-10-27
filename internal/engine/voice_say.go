package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/voice"
)

// voiceSay speaks text for any verified caller: the owner, root, a session
// or a worker. It uses the voice provider's account, so an unverified
// caller is refused.
func (s services) voiceSay(ctx context.Context, req voice.SayRequest) (voice.SayResult, error) {
	switch authority.Of(ctx).Role {
	case authority.Owner, authority.Root, authority.Session, authority.Worker:
	default:
		return voice.SayResult{}, errors.New("voice say needs a verified caller")
	}
	return s.say.Say(ctx, req)
}
