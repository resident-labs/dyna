package engine

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) computerEvent(ctx context.Context, session contract.SessionID, room contract.RoomID, state contract.ComputerState) error {
	e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.ComputerChanged{Session: session, To: state})
	if err != nil {
		return err
	}
	return s.store.RecordEvent(ctx, e)
}

func (s *Service) ComputerChanged(ctx context.Context, caller contract.Principal, req contract.ComputerChangedRequest) (contract.ComputerChangedResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ComputerChangedResponse{}, forbidden()
	}
	if req.To != contract.ComputerRunning && req.To != contract.ComputerStopped {
		return contract.ComputerChangedResponse{}, invalid("invalid computer state")
	}
	session, open, err := s.store.OpenSession(ctx, caller.Session)
	if err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	if !open {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	handle, problem := s.lockSession(ctx, session.ID)
	if problem != nil {
		return contract.ComputerChangedResponse{}, problem
	}
	defer handle.Unlock()
	if handle.Channel() == nil {
		return contract.ComputerChangedResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "box channel is not ready"}
	}
	if handle.ComputerState() == req.To {
		return contract.ComputerChangedResponse{}, nil
	}
	if err := s.computerEvent(ctx, session.ID, session.Room, req.To); err != nil {
		return contract.ComputerChangedResponse{}, failure(err)
	}
	handle.SetComputer(session.Room, req.To)
	return contract.ComputerChangedResponse{}, nil
}

// computerStartLimit covers the SSH connection and the desktop's start, which waits up to 10 seconds for its display.
const computerStartLimit = 30 * time.Second

// ComputerStart starts the desktop in the session's box for the owner, and returns once it runs. It runs the box's own
// `dyna computer start`, which the agent's computer actions also start the desktop with. It does not touch the turn.
func (s *Service) ComputerStart(ctx context.Context, caller contract.Principal, req contract.ComputerStartRequest) (contract.ComputerStartResponse, *contract.Error) {
	current, problem := s.readyBox(ctx, req.Session)
	if problem != nil {
		return contract.ComputerStartResponse{}, problem
	}
	argv, err := s.boxes.ExecArgv(ctx, current.Host, current.ID, "dyna", "computer", "start")
	if err != nil {
		return contract.ComputerStartResponse{}, failure(err)
	}
	ctx, cancel := context.WithTimeout(ctx, computerStartLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = 2 * time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		message := "the desktop did not start"
		if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); lines[len(lines)-1] != "" {
			message += ": " + strings.TrimPrefix(lines[len(lines)-1], "Error: ")
		}
		return contract.ComputerStartResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: message}
	}
	return contract.ComputerStartResponse{}, nil
}

// ComputerStream returns the command that carries the VNC stream of a session's desktop, and a release for when the view
// closes. An open view keeps the box awake, as an open terminal does. The command fails at once if the desktop is not
// running, so the view starts it first.
func (s *Service) ComputerStream(ctx context.Context, session contract.SessionID) ([]string, func(), *contract.Error) {
	host, id, release, problem := s.holdBox(ctx, session)
	if problem != nil {
		return nil, release, problem
	}
	argv, err := s.boxes.ExecArgv(ctx, host, id, "dyna", "computer", "stream")
	if err != nil {
		release()
		return nil, func() {}, failure(err)
	}
	return argv, release, nil
}
