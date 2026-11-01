package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) ShellWatch(ctx context.Context, id contract.SessionID) (<-chan contract.ShellJobOutput, *contract.Error) {
	session, open, err := s.store.OpenSession(ctx, id)
	if err != nil {
		return nil, failure(err)
	}
	if !open {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	b, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return nil, failure(err)
	}
	handle, problem := s.lockSession(ctx, id)
	if problem != nil {
		return nil, problem
	}
	defer handle.Unlock()
	if b.State != contract.BoxReady || handle.Channel() == nil {
		return nil, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	watch := handle.WatchShell()
	go func() {
		<-ctx.Done()
		if !handle.Lock() {
			return
		}
		defer handle.Unlock()
		handle.RemoveShellWatch(watch)
	}()
	return watch.Events(), nil
}

func (s *Service) ShellOutput(_ context.Context, caller contract.Principal, req contract.ShellOutputRequest) (contract.ShellOutputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ShellOutputResponse{}, forbidden()
	}
	handle, ok := s.lookupSession(caller.Session)
	if !ok {
		return contract.ShellOutputResponse{}, nil
	}
	defer handle.Unlock()
	handle.ShellOutput(req.Generation, req.Jobs)
	return contract.ShellOutputResponse{}, nil
}
