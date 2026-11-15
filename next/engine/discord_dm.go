package engine

import "github.com/resident-labs/dyna/contract"

func confinedDiscordRefusal() *contract.Error {
	return &contract.Error{Code: contract.ErrForbidden, Message: "this turn answers a member outside the team (or a DM); it can only read and answer in that channel"}
}
