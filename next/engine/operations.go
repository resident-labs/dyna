package engine

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

func (s *Service) ConfigureOperations(ctx context.Context, kinds map[contract.OpKind]approve.Entry, expiry time.Duration) error {
	s.approvals = &approve.Service{Store: s.store, Kinds: kinds, Context: ctx, Now: s.now, Expiry: expiry, ID: func(prefix string) (string, error) { return id(prefix, s.now()) }}
	return s.approvals.Recover(ctx)
}
func (s *Service) ExpireOperations(ctx context.Context) error { return s.approvals.Expire(ctx) }
func (s *Service) OpSubmit(ctx context.Context, caller contract.Principal, req contract.OpSubmitRequest) (contract.OpSubmitResponse, *contract.Error) {
	op, err := s.approvals.Submit(ctx, caller, req.Operation)
	if err != nil {
		return contract.OpSubmitResponse{}, failure(err)
	}
	return contract.OpSubmitResponse{Operation: op}, nil
}
func (s *Service) OpDecide(ctx context.Context, caller contract.Principal, req contract.OpDecideRequest) (contract.OpDecideResponse, *contract.Error) {
	err := s.approvals.Decide(ctx, caller, req.ID, req.Approve, req.Reason)
	if err != nil {
		return contract.OpDecideResponse{}, failure(err)
	}
	return contract.OpDecideResponse{}, nil
}
func (s *Service) OpWithdraw(ctx context.Context, caller contract.Principal, req contract.OpWithdrawRequest) (contract.OpWithdrawResponse, *contract.Error) {
	err := s.approvals.Withdraw(ctx, caller, req.ID)
	if err != nil {
		return contract.OpWithdrawResponse{}, failure(err)
	}
	return contract.OpWithdrawResponse{}, nil
}
func (s *Service) OpList(ctx context.Context, caller contract.Principal, req contract.OpListRequest) (contract.OpListResponse, *contract.Error) {
	ops, err := s.approvals.List(ctx, caller, req.Room)
	if err != nil {
		return contract.OpListResponse{}, failure(err)
	}
	return contract.OpListResponse{Operations: ops}, nil
}
