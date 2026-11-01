package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	boxhostexec "github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/hostexec"
	"github.com/resident-labs/dyna/engine/store"
	"os"
	"path/filepath"
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
	if req.Host == "" || len(req.Argv) == 0 || req.Argv[0] == "" || req.Timeout < 0 || req.Timeout > contract.DurationMS((2*time.Hour).Milliseconds()) {
		return contract.HostexecRunResponse{}, invalid("invalid host, command or timeout")
	}
	grant, err := s.matchHostexec(ctx, session.Room, who, req.Host)
	if errors.Is(err, hostexec.ErrNoGrant) {
		return contract.HostexecRunResponse{}, &contract.Error{Code: contract.ErrNoGrant, Message: "no matching host-exec grant"}
	}
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	a := contract.HostexecRunArgs{Room: session.Room, Host: req.Host, Argv: req.Argv, Cwd: req.Cwd, Timeout: req.Timeout, Grant: grant.ID}
	op, err := s.approvals.Submit(ctx, who, contract.Operation{Room: session.Room, Args: a})
	if err != nil {
		return contract.HostexecRunResponse{}, failure(err)
	}
	return contract.HostexecRunResponse{Operation: op}, nil
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
			if err != nil && !errors.Is(err, boxhostexec.ErrHostBusy) {
				stopped = store.HostexecLost
			} else if err == nil && (code == 137 || code == 143) && !s.now().Before(started.Add(time.Duration(a.Timeout)*time.Millisecond)) {
				// A signal-like exit at the deadline is treated as RuntimeMaxSec expiry.
				stopped = store.HostexecTimeout
			}
			if e := s.store.FinishHostexec(ctx, op, s.now(), len(out), stopped); e != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: e.Error()}
			}
			result := contract.OpResult{ExitCode: code, Truncated: truncated}
			if err != nil {
				result.Error = err.Error()
				result.Cause = contract.CauseHostLost
				if errors.Is(err, boxhostexec.ErrHostBusy) {
					result.Cause = contract.CauseHostBusy
				}
			}
			if len(out) > 64<<10 {
				rest := out[64<<10:]
				sum := sha256.Sum256(rest)
				name := hex.EncodeToString(sum[:])
				if e := os.MkdirAll(s.filesDir, 0700); e != nil {
					return contract.OpResult{Cause: contract.CauseInternal, Error: e.Error()}
				}
				if e := os.WriteFile(filepath.Join(s.filesDir, name), rest, 0600); e != nil {
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
