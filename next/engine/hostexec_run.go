package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	boxhostexec "github.com/resident-labs/dyna/box/hostexec"
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

// SetHostPromoter gives host set the full host check that an exec-only host passes before it takes boxes or runners.
func (s *Service) SetHostPromoter(f func(context.Context, contract.HostName, bool) error) {
	s.promoteHost = f
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
	if n := boxhostexec.QuotedLen([]string{"bash", "-c", req.Command}); n > boxhostexec.MaxQuoted {
		return contract.HostexecRunResponse{}, &contract.Error{Code: contract.ErrCommandTooLong, Message: fmt.Sprintf("the command takes %d bytes once quoted for the host; one environment string holds at most %d", n, boxhostexec.MaxQuoted)}
	}
	grant, err := s.matchHostexec(ctx, session.Room, who, req.Host)
	if errors.Is(err, hostexec.ErrNoGrant) {
		return contract.HostexecRunResponse{}, &contract.Error{Code: contract.ErrNoGrant, Message: fmt.Sprintf("no host-exec grant for %s; ask with hostexec request_grant, then run it again once approved", req.Host)}
	}
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	a := contract.HostexecRunArgs{Room: session.Room, Host: req.Host, Argv: []string{"bash", "-c", req.Command}, Cwd: req.Cwd, Timeout: req.Timeout, Grant: grant.ID}
	op, err := s.approvals.SubmitWithClientID(ctx, who, contract.Operation{Room: session.Room, Kind: a.OpKind(), Args: a}, req.ClientID)
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	// A request that repeats a client ID gets the run it started, whichever process runs it, and waits for it.
	if op.Result == nil && ctx.Err() == nil {
		waited, err := s.approvals.Wait(ctx, op.ID)
		if err != nil {
			return contract.HostexecRunResponse{}, failure(err)
		}
		waited.Args = op.Args
		op = waited
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
	check := func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
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
	}
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
		Check: check,
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
			return s.finishHostexec(ctx, op, a, s.now(), false)
		},
		Resume: func(ctx context.Context, op contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
			a := args.(contract.HostexecRunArgs)
			run, err := s.store.ReadHostexec(ctx, op)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			if err := check(ctx, args, who); err != nil {
				// The grant ended while the engine was away. The command is stopped like a session's stop (the sweep does it), and the
				// run's folder stays on the host, readable only by its user.
				if _, _, err := s.store.StopHostexecRun(ctx, op); err != nil {
					return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
				}
				return contract.OpResult{Error: "stale: " + err.Error(), Cause: contract.CauseStale}
			}
			return s.finishHostexec(ctx, op, a, run.Started, true)
		},
	}
}

// finishHostexec runs a host run, or with resumed set follows the one a previous engine started, and records its end. A resumed run
// that the host cannot be asked about is asked again until the run's own time is up. The caller's context ending is the engine
// shutting down: the run is left as it is, for the next start to follow.
func (s *Service) finishHostexec(ctx context.Context, op contract.OpID, a contract.HostexecRunArgs, started time.Time, resumed bool) contract.OpResult {
	runCtx, cancel := s.hostexecTimeout(ctx, started.Add(time.Duration(a.Timeout)*time.Millisecond+15*time.Second).Sub(s.now()))
	a.Attach = resumed
	out, code, truncated, err := s.hostexec.RunHostexec(runCtx, a, op)
	for delay := 500 * time.Millisecond; resumed && err != nil && runCtx.Err() == nil; delay = min(2*delay, 10*time.Second) {
		select {
		case <-time.After(delay):
		case <-runCtx.Done():
		}
		out, code, truncated, err = s.hostexec.RunHostexec(runCtx, a, op)
	}
	cancel()
	if ctx.Err() != nil {
		return contract.OpResult{Cause: contract.CauseInterrupted}
	}
	a.Attach = false
	lost := err != nil || resumed && code == boxhostexec.NoRunStatus && string(out) == boxhostexec.NoRunOutput(op)
	stopped := store.HostexecStop("")
	if lost {
		stopped = store.HostexecLost
		code = 0
	} else if (code == 137 || code == 143) && !s.now().Before(started.Add(time.Duration(a.Timeout)*time.Millisecond)) {
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
	} else if lost && resumed {
		result.Error = fmt.Sprintf("the engine restarted during this host run (deploy); the command may still be running on %s as %s", a.Host, "dyna-hostexec-"+string(op)+".scope")
		result.Cause = contract.CauseEngineRestarted
	} else if lost {
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
	// The folder is read back and removed whatever became of the run, so a stopped or lost run leaves none behind.
	collectCtx, cancel := s.hostexecTimeout(context.WithoutCancel(ctx), 2*time.Minute+15*time.Second)
	tar, collectCode, tarTruncated, e := s.hostexec.RunHostexec(collectCtx, boxhostexec.CollectArgs(a, op), op+"-out")
	cancel()
	switch {
	case e != nil || collectCode != 0:
		result.Output += "\n[$DYNA_OUT could not be read back, and may remain on the host]"
	case len(tar) == 0:
	case tarTruncated:
		result.Output += "\n[files in $DYNA_OUT exceed 8 MiB: not copied back]"
	default:
		result.OutFiles, _, e = s.saveFile(ctx, a.Room, bytes.NewReader(tar), int64(len(tar)))
		if e != nil {
			return contract.OpResult{Cause: contract.CauseInternal, Error: e.Error()}
		}
	}
	return result
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
	grants, err := s.store.ListHostexecGrants(ctx, s.now())
	if err != nil {
		return contract.HostexecListResponse{}, failure(err)
	}
	return contract.HostexecListResponse{Grants: grants}, nil
}
