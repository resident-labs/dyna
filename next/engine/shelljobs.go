package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

// ShellJobEnded records the end of a background shell job and wakes its session once.
// A retry of the same job changes nothing. A running turn takes the wake as a steer.
func (s *Service) ShellJobEnded(ctx context.Context, caller contract.Principal, req contract.ShellJobEndedRequest) (contract.ShellJobEndedResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ShellJobEndedResponse{}, forbidden()
	}
	if req.Job == "" {
		return contract.ShellJobEndedResponse{}, invalid("job is required")
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ShellJobEndedResponse{}, failure(err)
	}
	event, err := s.event(session.Room, caller, contract.ShellJobEnded{Session: session.ID, Job: req.Job, Description: req.Description, End: req.End, ExitCode: req.ExitCode, Tail: req.Tail})
	if err != nil {
		return contract.ShellJobEndedResponse{}, failure(err)
	}
	if err = s.store.ShellJobEnded(ctx, session.ID, req.Job, event); err != nil {
		return contract.ShellJobEndedResponse{}, failure(err)
	}
	return contract.ShellJobEndedResponse{}, nil
}

// ShellJobRunning tells a session about a background job of its own that is still running.
func (s *Service) ShellJobRunning(ctx context.Context, caller contract.Principal, req contract.ShellJobRunningRequest) (contract.ShellJobRunningResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ShellJobRunningResponse{}, forbidden()
	}
	if req.Job == "" {
		return contract.ShellJobRunningResponse{}, invalid("job is required")
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ShellJobRunningResponse{}, failure(err)
	}
	event, err := s.event(session.Room, caller, contract.ShellJobRunning{Session: session.ID, Job: req.Job, Description: req.Description, RanMinutes: req.RanMinutes, QuietMinutes: req.QuietMinutes, Tail: req.Tail})
	if err != nil {
		return contract.ShellJobRunningResponse{}, failure(err)
	}
	if err = s.store.ShellJobRunning(ctx, session.ID, req.Job, event); err != nil {
		return contract.ShellJobRunningResponse{}, failure(err)
	}
	return contract.ShellJobRunningResponse{}, nil
}
