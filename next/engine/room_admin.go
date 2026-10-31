package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) roomAdmin(ctx context.Context, caller contract.Principal, room contract.RoomID) *contract.Error {
	if problem := s.roomAccess(ctx, caller, room); problem != nil {
		return problem
	}
	if caller.Kind == contract.PrincipalOwner {
		return nil
	}
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		return failure(err)
	}
	if tree.Root() != caller.Session {
		return forbidden()
	}
	return nil
}
