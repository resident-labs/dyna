package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

const enforceCallers = true

// socketCallers identifies the engine's socket callers for websvc.
type socketCallers struct {
	resolver callerid.Resolver
	resolve  func(net.Conn) callerid.Identity
	sessions *subagent.Store
	// runRoom gives a workflow run's room ID from its stored row. Nil reads
	// runstore.
	runRoom            func(run string) (string, error)
	coordinatorChanged func(string)
	refused            atomic.Int64
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
	return websvc.Caller{}, errors.New(id.Reason)
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
		// Only the coordinator is a worker; each workflow agent is a session.
		if caller.Agent != 0 {
			s.refused.Add(1)
			return authority.Actor{}, fmt.Errorf("workflow agent %d acts as a session, not as a worker", caller.Agent)
		}
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
		return authority.Actor{}, err
	case websvc.Session:
		actor, err := s.sessions.SessionActor(caller.Session)
		if err == nil {
			return actor, nil
		}
		s.refused.Add(1)
		return authority.Actor{}, err
	case websvc.Unverified:
		s.refused.Add(1)
		return authority.Actor{}, errors.New("the engine could not verify this caller")
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

// Refused counts the callers the rules refused since the engine started.
func (s *socketCallers) Refused() int64 {
	if s == nil {
		return 0
	}
	return s.refused.Load()
}

// identifyCallers builds the socket resolver from committed launch records.
// A read-only engine without the launch store has no recorded callers.
func identifyCallers(ctx context.Context, dataDir string, readOnly bool, sessions *subagent.Store) (*socketCallers, error) {
	var records callerid.Records = callerid.Empty{}
	store, err := callerid.OpenStore(dataDir)
	if err == nil {
		records = store
	} else if !readOnly {
		return nil, err
	}
	r, err := callerid.New("/proc", os.Getpid(), records)
	if err != nil {
		return nil, err
	}
	return &socketCallers{resolver: r, resolve: r.ResolveConn, sessions: sessions}, nil
}
