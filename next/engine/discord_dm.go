package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func directMessageRefusal() *contract.Error {
	return &contract.Error{Code: contract.ErrForbidden, Message: "a direct message is answered in that DM; act in the server only from a server message"}
}

// discordDMWriteScope confines a turn answering a DM to that input channel.
func (s *Service) discordDMWriteScope(ctx context.Context, caller contract.Principal, channel string, api bool) (bool, *contract.Error) {
	inputs, err := s.store.DiscordTurnInputs(ctx, caller.Session)
	if err != nil {
		return false, failure(err)
	}
	hasDM, targetDM := false, false
	for _, m := range inputs {
		if m.Direct {
			hasDM = true
			targetDM = targetDM || m.Channel == channel
		}
	}
	if !hasDM {
		return false, nil
	}
	if targetDM && !api {
		return true, nil
	}
	return false, directMessageRefusal()
}
