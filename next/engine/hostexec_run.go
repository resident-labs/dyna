package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/hostexec"
	"github.com/resident-labs/dyna/engine/store"
	"strings"
	"time"
)

type HostexecRunner interface {
	RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error)
	StopHostexec(context.Context, contract.HostName, string) error
}

func (s *Service) SetHostexecRunner(r HostexecRunner) { s.hostexec = r }

func (s *Service) matchHostexec(ctx context.Context, room contract.RoomID, who contract.Principal, host contract.HostName) (contract.Grant, error) {
	grants, err := s.store.ListActiveGrants(ctx, room, who, s.now())
	if err != nil {
		return contract.Grant{}, err
	}
	return hostexec.Match(grants, host, s.now())
}

func (s *Service) HostexecRun(ctx context.Context, who contract.Principal, req contract.HostexecRunRequest) (contract.HostexecRunResponse, *contract.Error) {
	if who.Kind != contract.PrincipalSession {
		return contract.HostexecRunResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, who.Session)
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	if req.Timeout == 0 {
		req.Timeout = contract.DurationMS((10 * time.Minute).Milliseconds())
	}
	if req.Host == "" || strings.TrimSpace(req.Command) == "" || req.Timeout < 0 || req.Timeout > contract.DurationMS((2*time.Hour).Milliseconds()) {
		return contract.HostexecRunResponse{}, invalid("invalid host, command or timeout")
	}
	if len(req.ClientID) > 64 {
		return contract.HostexecRunResponse{}, invalid("client_id is at most 64 characters")
	}
	grant, err := s.matchHostexec(ctx, session.Room, who, req.Host)
	if errors.Is(err, hostexec.ErrNoGrant) {
		return contract.HostexecRunResponse{}, &contract.Error{Code: contract.ErrNoGrant, Message: "no matching host-exec grant"}
	}
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	a := contract.HostexecRunArgs{Room: session.Room, Host: req.Host, Argv: []string{"bash", "-c", req.Command}, Cwd: req.Cwd, Timeout: req.Timeout, Grant: grant.ID}
	op, err := s.approvals.SubmitWithClientID(ctx, who, contract.Operation{Room: session.Room, Args: a}, req.ClientID)
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	return contract.HostexecRunResponse{Operation: op}, nil
}

// hostexecStopWait is how long a stop waits for a run that has been submitted but not started.
const hostexecStopWait = 2 * time.Second

// HostexecStop stops a session's own run, as job stop asks. The run's scope is stopped on the host at once, the run is stored
// as stopped, and a stop that fails here is retried by the sweep. A run that has ended is left alone.
func (s *Service) HostexecStop(ctx context.Context, who contract.Principal, req contract.HostexecStopRequest) (contract.HostexecStopResponse, *contract.Error) {
	if who.Kind != contract.PrincipalSession {
		return contract.HostexecStopResponse{}, forbidden()
	}
	if req.ClientID == "" || len(req.ClientID) > 64 {
		return contract.HostexecStopResponse{}, invalid("client_id names the run, in at most 64 characters")
	}
	record, err := s.store.ReadReplayOperation(ctx, who, req.ClientID)
	if err != nil {
		return contract.HostexecStopResponse{}, failure(err)
	}
	if record.Operation.ID == "" || record.Operation.Kind != (contract.HostexecRunArgs{}).OpKind() {
		return contract.HostexecStopResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "no host run of this session has that client_id"}
	}
	for deadline := time.Now().Add(hostexecStopWait); ; {
		run, ok, err := s.store.StopHostexecRun(ctx, record.Operation.ID)
		if err != nil {
			return contract.HostexecStopResponse{}, failure(err)
		}
		if ok {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = s.hostexec.StopHostexec(stopCtx, run.Host, run.Scope)
			cancel()
			return contract.HostexecStopResponse{}, nil
		}
		// A run that has not started gets a moment to start, since the job server stops it the moment its job is stopped.
		if run.Op != "" || !time.Now().Before(deadline) {
			return contract.HostexecStopResponse{}, nil
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return contract.HostexecStopResponse{}, failure(ctx.Err())
		}
	}
}
func (s *Service) HostexecRunEntry() approve.Entry {
	return approve.Entry{
		Scope: contract.ScopeTerminal,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassAuto },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			a := args.(contract.HostexecRunArgs)
			body := fmt.Sprintf("timeout: %s", time.Duration(a.Timeout)*time.Millisecond)
			if a.Cwd != "" {
				body = fmt.Sprintf("Working directory: %s; ", quoteHostexec(a.Cwd)) + body
			}
			return contract.Card{Title: "asks to run a command", Target: contract.Target{Kind: contract.TargetHost, Name: quoteHostexec(string(a.Host))}, Command: a.Argv, Body: body}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			a := args.(contract.HostexecRunArgs)
			grants, err := s.store.ListActiveGrants(ctx, a.Room, who, s.now())
			if err != nil {
				return err
			}
			for _, grant := range grants {
				if grant.ID == a.Grant {
					_, err = hostexec.Match([]contract.Grant{grant}, a.Host, s.now())
					return err
				}
			}
			return hostexec.ErrNoGrant
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var a contract.HostexecRunArgs
			err := json.Unmarshal(data, &a)
			return a, err
		},
		Execute: func(ctx context.Context, op contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
			a := args.(contract.HostexecRunArgs)
			run := store.HostexecRun{Op: op, Grant: a.Grant, Host: a.Host, Scope: "dyna-hostexec-" + string(op) + ".scope", Timeout: a.Timeout}
			if err := s.store.StartHostexec(ctx, run, who, a.Room, s.now()); err != nil {
				if errors.Is(err, store.ErrGrantInactive) {
					return contract.OpResult{Cause: contract.CauseGrantInactive, Error: "grant no longer active"}
				}
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			started := s.now()
			runCtx, cancel := s.hostexecTimeout(ctx, time.Duration(a.Timeout)*time.Millisecond+15*time.Second)
			out, code, truncated, err := s.hostexec.RunHostexec(runCtx, a, op)
			cancel()
			stopped := store.HostexecStop("")
			if err != nil {
				stopped = store.HostexecLost
			} else if err == nil && (code == 137 || code == 143) && !s.now().Before(started.Add(time.Duration(a.Timeout)*time.Millisecond)) {
				// A signal-like exit at the deadline is treated as RuntimeMaxSec expiry.
				stopped = store.HostexecTimeout
			}
			final, e := s.store.FinishHostexec(ctx, op, s.now(), len(out), stopped)
			if e != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: e.Error()}
			}
			result := contract.OpResult{ExitCode: code, Truncated: truncated}
			if final == store.HostexecStopped {
				result.Error = "stopped by its session"
			} else if err != nil {
				result.Error = err.Error()
				result.Cause = contract.CauseHostLost
			}
			if len(out) > 64<<10 {
				rest := out[64<<10:]
				name, _, e := s.saveFile(ctx, a.Room, bytes.NewReader(rest), int64(len(rest)))
				if e != nil {
					return contract.OpResult{Cause: contract.CauseInternal, Error: e.Error()}
				}
				result.OutputFile = name
				out = out[:64<<10]
			}
			result.Output = strings.ToValidUTF8(string(out), "�")
			return result
		},
	}
}
func (s *Service) StopHostexecs(ctx context.Context) error {
	runs, err := s.store.HostexecStops(ctx, s.now())
	if err != nil {
		return err
	}
	for _, r := range runs {
		// Stops time out before the next tick, which retries any failed stop.
		go func() {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if s.hostexec.StopHostexec(stopCtx, r.Host, r.Scope) == nil {
				_ = s.store.CompleteHostexecStop(stopCtx, r.Op, s.now())
			}
		}()
	}
	return nil
}

func (s *Service) HostexecList(ctx context.Context, who contract.Principal, _ contract.HostexecListRequest) (contract.HostexecListResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.HostexecListResponse{}, forbidden()
	}
	grants, err := s.store.ListHostexecGrants(ctx, s.now())
	if err != nil {
		return contract.HostexecListResponse{}, failure(err)
	}
	return contract.HostexecListResponse{Grants: grants}, nil
}
