package engine

import (
	"context"
	"fmt"

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

func shellJobText(job contract.ShellJobEnded) string {
	name := string(job.Job)
	if job.Description != "" {
		name = "'" + job.Description + "'"
	}
	result := fmt.Sprintf("exited with code %d", job.ExitCode)
	if job.End != nil {
		switch job.End.Kind {
		case contract.ShellJobExited:
			result = fmt.Sprintf("exited with code %d", job.End.Code)
		case contract.ShellJobSignaled:
			result = fmt.Sprintf("ended by a signal (%s)", job.End.Signal)
		case contract.ShellJobStopped:
			result = "was stopped"
		}
	}
	return fmt.Sprintf("Background job %s %s. Last output:\n%s", name, result, job.Tail)
}
