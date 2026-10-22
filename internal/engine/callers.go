package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// TODO: legacy/shim remove later — report-only for the first release of item 3: a caller the rules would refuse is logged, counted in Health and served as before, so a wrong refusal cannot lock out root and every agent at once; set to true and remove this marker once a day of logs shows no unexpected refusal.
const enforceCallers = false

// socketCallers identifies the engine's socket callers for websvc.
type socketCallers struct {
	resolver callerid.Resolver
	resolve  func(net.Conn) callerid.Identity
	sessions *subagent.Store
	// runRoom gives a workflow run's room ID from its stored row. Nil reads
	// runstore.
	runRoom            func(run string) (string, error)
	enforce            bool
	coordinatorChanged func(string)
	refused            atomic.Int64
	logf               func(string, ...any)
}

func (s *socketCallers) identify(c net.Conn) (websvc.Caller, error) {
	id := s.resolve(c)
	if id.Kind != callerid.Refused {
		if id.Kind == callerid.Worker && id.Run != "" && id.Session == "" && id.Agent == 0 && s.coordinatorChanged != nil {
			s.coordinatorChanged(id.Run)
		}
		return websvc.Caller{Kind: websvc.CallerKind(id.Kind), Session: id.Session, Run: id.Run, Agent: id.Agent}, nil
	}
	s.refused.Add(1)
	if s.enforce {
		return websvc.Caller{}, errors.New(id.Reason)
	}
	s.logf("callers: report-only, would refuse a caller: %s", id.Reason)
	return websvc.Caller{Kind: websvc.Unverified}, nil
}

// actForCallers makes each call act for its caller: the authority checks
// it reaches read the actor with authority.Of (requirements item 3). A paired
// device or a Connect user is the owner's.
func (s *socketCallers) actForCallers(calls map[string]websvc.Call) {
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			actor := authority.Actor{Role: authority.Owner}
			if caller, local := websvc.CallerFromContext(ctx); local {
				var err error
				if actor, err = s.actor(caller); err != nil {
					return nil, err
				}
			}
			return call(authority.WithActor(ctx, actor), args)
		}
	}
}

// actor is what a local caller acts for.
func (s *socketCallers) actor(caller websvc.Caller) (authority.Actor, error) {
	switch caller.Kind {
	case websvc.Owner:
		return authority.Actor{Role: authority.Owner}, nil
	case websvc.Worker:
		actor, err := authority.WorkerActor(caller.Run, caller.Agent)
		if err != nil {
			return authority.Actor{}, err
		}
		// A worker acts in its run's room, taken from the run's stored row,
		// never from a call argument or a claim (item 3).
		room, err := s.roomOfRun(caller.Run)
		if err == nil {
			actor.Room = room
			return actor, nil
		}
		s.refused.Add(1)
		if s.enforce {
			return authority.Actor{}, err
		}
		// Report-only keeps today's power: a known worker with no room, never
		// the engine's own actor.
		s.logf("callers: report-only, would refuse a worker of run %s: %v", caller.Run, err)
		return actor, nil
	case websvc.Session:
		actor, err := s.sessions.SessionActor(caller.Session)
		if err == nil {
			return actor, nil
		}
		s.refused.Add(1)
		if s.enforce {
			return authority.Actor{}, err
		}
		s.logf("callers: report-only, would refuse session %s: %v", caller.Session, err)
		return unverifiedActor(), nil
	case websvc.Unverified:
		return unverifiedActor(), nil
	}
	return authority.Actor{}, fmt.Errorf("the engine cannot act for a %q caller", caller.Kind)
}

// roomOfRun is the room ID in a run's stored row. A run outside any room has
// none, so its workers act in no room.
func (s *socketCallers) roomOfRun(run string) (string, error) {
	if s.runRoom != nil {
		return s.runRoom(run)
	}
	meta, err := runstore.ReadMeta(run)
	if err != nil {
		return "", err
	}
	return meta.RoomID, nil
}

// TODO: legacy/shim remove later — report-only for the first release of item 3: a caller the rules would refuse keeps today's power, the engine process's own actor; remove with enforceCallers, when no caller is unverified.
func unverifiedActor() authority.Actor { return authority.Current() }

// Refused counts the callers the rules refused since the engine started.
func (s *socketCallers) Refused() int64 {
	if s == nil {
		return 0
	}
	return s.refused.Load()
}

// identifyCallers builds the one Resolver for the private socket
// (requirements item 3). It first records the live work that started before
// launches were recorded: a writable engine in its store, a read-only one in
// memory for this start. A read-only engine may have no records table; it
// then knows only the work it found.
func identifyCallers(ctx context.Context, dataDir string, readOnly bool, sessions *subagent.Store) (*socketCallers, error) {
	memory := &callerid.Memory{}
	records := callerid.Layered{memory}
	store, openErr := callerid.OpenStore(dataDir)
	switch {
	case openErr == nil:
		records = callerid.Layered{store, memory}
	case !readOnly:
		return nil, openErr
	}
	backfillCallers(sessions, func(pid int, actor callerid.Identity, launcher string) error {
		if openErr == nil && !readOnly {
			return store.Backfill(ctx, pid, actor, launcher)
		}
		return memory.Backfill(pid, actor, launcher)
	})
	r, err := callerid.New("/proc", os.Getpid(), records)
	if err != nil {
		return nil, err
	}
	return &socketCallers{resolver: r, resolve: r.ResolveConn, sessions: sessions, enforce: enforceCallers, logf: log.Printf}, nil
}

// TODO: legacy/shim remove later — records the runners, shell supervisors and workflow workers that were already running when launch records began (#1418), so their agents are not refused after the install; remove with item 31's legacy scan, when no such process is alive.
// backfillCallers records live work from the engine's own rows only: a
// session's runner PID, a shell job's supervisor and a run's worker, each
// checked against its command line. It never records the owner.
func backfillCallers(sessions *subagent.Store, backfill func(pid int, actor callerid.Identity, launcher string) error) {
	record := func(pid int, actor callerid.Identity, launcher string, args ...string) {
		if pid <= 0 || !runs(pid, args...) {
			return
		}
		if err := backfill(pid, actor, launcher); err != nil {
			log.Printf("callers: backfill %s %d: %v", launcher, pid, err)
		}
	}
	if infos, err := sessions.List(false); err == nil {
		for _, info := range infos {
			record(info.Pid, callerid.Identity{Kind: callerid.Session, Session: info.ID}, "turn", "subagent", "_turn", info.ID)
		}
	}
	if jobs, err := shells.DefaultStore().List(false); err == nil {
		for _, job := range jobs {
			if sessionlife.IsSessionID(job.Owner) {
				record(job.Supervisor, callerid.Identity{Kind: callerid.Session, Session: job.Owner}, "shell", "shell", "_run", job.ID)
			}
		}
	}
	if metas, err := runstore.List(); err == nil {
		for _, meta := range metas {
			if meta.Status == "running" {
				record(meta.Pid, callerid.Identity{Kind: callerid.Worker, Run: meta.ID}, "workflow", "run-worker")
			}
		}
	}
}

// runs reports whether pid's command line holds args in order, right after
// the program name, so a reused PID of another program never matches.
func runs(pid int, args ...string) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	argv := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	return len(argv) > len(args) && slices.Equal(argv[1:1+len(args)], args)
}
