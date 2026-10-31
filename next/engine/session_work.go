package engine

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
)

func (s *Service) SessionWork(ctx context.Context, caller contract.Principal, req contract.SessionWorkRequest) (contract.SessionWorkResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.SessionWorkResponse{}, forbidden()
	}
	if req.Session == "" || req.Limit < 0 || req.Limit > 100 {
		return contract.SessionWorkResponse{}, invalid("invalid session work request")
	}
	if req.Limit == 0 {
		req.Limit = 30
	}
	if _, err := s.store.Session(ctx, req.Session); err != nil {
		return contract.SessionWorkResponse{}, failure(err)
	}
	turns, more, total, err := s.store.SessionWork(ctx, req.Session, req.Before, req.Limit)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.SessionWorkResponse{}, invalid("invalid turn cursor")
	}
	if err != nil {
		return contract.SessionWorkResponse{}, failure(err)
	}
	return contract.SessionWorkResponse{Turns: turns, More: more, Total: total}, nil
}
