package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	poolScriptBytes = 64 << 10
	poolIdle        = 30 * time.Minute
	// The store reads idle back as a time.Duration, which holds at most about 2562047 hours.
	poolIdleMost  = 2562047 * time.Hour
	poolMaxTooLow = "a pool needs a max of at least 1"
)

// checkIdle refuses an idle time the step could not read. Zero is the default, 30 minutes.
func checkIdle(idle contract.DurationMS) *contract.Error {
	switch {
	case idle < 0:
		return invalid("idle cannot be negative")
	case idle > contract.DurationMS(poolIdleMost.Milliseconds()):
		return invalid(fmt.Sprintf("idle is at most %d hours", int64(poolIdleMost/time.Hour)))
	}
	return nil
}

// idleTime is a checked idle time, zero making the default.
func idleTime(idle contract.DurationMS) time.Duration {
	if idle == 0 {
		return poolIdle
	}
	return time.Duration(idle) * time.Millisecond
}

var poolName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (s *Service) checkPool(ctx context.Context, req contract.PoolAddRequest) *contract.Error {
	switch {
	case len(req.Name) > 32 || !poolName.MatchString(req.Name):
		return invalid("a pool name is at most 32 lowercase letters, digits and single hyphens")
	case !strings.HasPrefix(req.Script, "#!"):
		return invalid("a pool's script starts with #!, as it runs by itself")
	case len(req.Script) > poolScriptBytes:
		return invalid(fmt.Sprintf("a pool's script is at most %d KiB", poolScriptBytes>>10))
	case req.Max < 1:
		return invalid(poolMaxTooLow)
	}
	if problem := checkIdle(req.Idle); problem != nil {
		return problem
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return failure(err)
	}
	if slices.ContainsFunc(hosts, func(h box.Host) bool { return string(h.Name) == req.Name }) {
		return invalid("a host has that name, and a room's rule could not tell them apart")
	}
	return nil
}

func (s *Service) addPool(ctx context.Context, req contract.PoolAddRequest) error {
	// No room names the pool yet, so nothing runs its script before the file is there.
	if err := s.store.AddPool(ctx, store.Pool{Name: req.Name, Max: req.Max, PerSession: req.PerSession, Idle: idleTime(req.Idle)}); err != nil {
		return err
	}
	path := s.layout.PoolScript(req.Name)
	err := os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		err = os.WriteFile(path, []byte(req.Script), 0o700)
	}
	if err != nil {
		if removeErr := s.store.RemovePool(ctx, req.Name); removeErr != nil {
			return fmt.Errorf("%w; %v", err, removeErr)
		}
	}
	return err
}

// asker gives the room a session asks from, and no room for the owner, who acts at once.
func (s *Service) asker(ctx context.Context, caller contract.Principal) (contract.RoomID, *contract.Error) {
	own, _, e := s.navScope(ctx, caller)
	return own, e
}

// PoolAdd adds a pool. It runs on the engine host and costs money, so a session's request waits for the owner.
func (s *Service) PoolAdd(ctx context.Context, caller contract.Principal, req contract.PoolAddRequest) (contract.PoolAddResponse, *contract.Error) {
	room, problem := s.asker(ctx, caller)
	if problem != nil {
		return contract.PoolAddResponse{}, problem
	}
	if problem := s.checkPool(ctx, req); problem != nil {
		return contract.PoolAddResponse{}, problem
	}
	if room != "" {
		op, err := s.approvals.Submit(ctx, caller, contract.Operation{Room: room, Kind: req.OpKind(), Args: req})
		if err != nil {
			return contract.PoolAddResponse{}, failure(err)
		}
		return contract.PoolAddResponse{Operation: op.ID}, nil
	}
	if err := s.addPool(ctx, req); err != nil {
		return contract.PoolAddResponse{}, failure(err)
	}
	return contract.PoolAddResponse{}, nil
}

// ownerEntry is a session's change that the owner approves, and the room's root when its setting for the kind is on.
func ownerEntry[A contract.OpArgs](root contract.RootApproval, card func(A) contract.Card, check func(context.Context, A) *contract.Error, apply func(context.Context, A) (string, error)) approve.Entry {
	return approve.Entry{
		Scope:        contract.ScopeAdmin,
		RootApproves: root,
		Class:        func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			return card(args.(A))
		},
		Check: func(ctx context.Context, args contract.OpArgs, _ contract.Principal) error {
			if problem := check(ctx, args.(A)); problem != nil {
				return fmt.Errorf("%w: %s", approve.ErrInvalid, problem.Message)
			}
			return nil
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var input A
			err := json.Unmarshal(data, &input)
			return input, err
		},
		Execute: func(ctx context.Context, _ contract.OpID, args contract.OpArgs, _ contract.Principal) contract.OpResult {
			output, err := apply(ctx, args.(A))
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			return contract.OpResult{Output: output}
		},
	}
}

// PoolAddEntry registers a session's pool. The card shows the script, which is what the owner approves.
func (s *Service) PoolAddEntry() approve.Entry {
	return ownerEntry(contract.RootApprovesPools, func(req contract.PoolAddRequest) contract.Card {
		shape := fmt.Sprintf("Shared by sessions, at most %d nodes.", req.Max)
		if req.PerSession {
			shape = fmt.Sprintf("One node per session, at most %d.", req.Max)
		}
		return contract.Card{Title: "asks to add the node pool " + req.Name, Body: shape + " Its script runs on the engine host:\n\n" + req.Script}
	}, s.checkPool, func(ctx context.Context, req contract.PoolAddRequest) (string, error) {
		return "Pool " + req.Name + " added.", s.addPool(ctx, req)
	})
}

func (s *Service) PoolList(ctx context.Context, _ contract.Principal, _ contract.PoolListRequest) (contract.PoolListResponse, *contract.Error) {
	pools, err := s.store.Pools(ctx)
	if err != nil {
		return contract.PoolListResponse{}, failure(err)
	}
	nodes, err := s.store.PoolNodes(ctx)
	if err != nil {
		return contract.PoolListResponse{}, failure(err)
	}
	result := contract.PoolListResponse{Pools: []contract.Pool{}}
	for _, pool := range pools {
		item := contract.Pool{Name: pool.Name, Max: pool.Max, PerSession: pool.PerSession, Problem: pool.Problem}
		if !pool.PerSession {
			item.Idle = contract.DurationMS(pool.Idle.Milliseconds())
		}
		for _, node := range nodes {
			if node.Pool == pool.Name {
				item.Nodes++
			}
		}
		result.Pools = append(result.Pools, item)
	}
	return result, nil
}

// PoolRemove removes an empty pool that no room names. Only the owner reaches it.
func (s *Service) PoolRemove(ctx context.Context, caller contract.Principal, req contract.PoolRemoveRequest) (contract.PoolRemoveResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.PoolRemoveResponse{}, forbidden()
	}
	if err := s.store.RemovePool(ctx, req.Name); err != nil {
		return contract.PoolRemoveResponse{}, failure(err)
	}
	// The row held a checked name, so this stays inside the pools folder.
	if err := os.RemoveAll(s.layout.Pool(req.Name)); err != nil {
		return contract.PoolRemoveResponse{}, failure(err)
	}
	return contract.PoolRemoveResponse{}, nil
}

func (s *Service) checkRoomHosts(ctx context.Context, req contract.RoomHostsRequest) *contract.Error {
	if err := s.store.RoomOpen(ctx, req.Room); err != nil {
		return failure(err)
	}
	pools, err := s.store.Pools(ctx)
	if err != nil {
		return failure(err)
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return failure(err)
	}
	named := 0
	for i, name := range req.Only {
		pool := slices.ContainsFunc(pools, func(p store.Pool) bool { return p.Name == name })
		if pool {
			named++
		}
		if !pool && !slices.ContainsFunc(hosts, func(h box.Host) bool { return string(h.Name) == name && h.Pool == "" }) || slices.Contains(req.Only[:i], name) {
			return invalid(name + " is not a pool or a host, or it comes twice")
		}
	}
	if named > 1 {
		return invalid("a room's rule names at most one pool")
	}
	return nil
}

func (s *Service) setRoomHosts(ctx context.Context, req contract.RoomHostsRequest) error {
	if err := s.store.SetRoomOnly(ctx, req.Room, req.Only); err != nil {
		return err
	}
	s.reconciler.Kick()
	return nil
}

// RoomHosts replaces where a room's new boxes may go. A session asks for its own room, and its request waits for the owner.
func (s *Service) RoomHosts(ctx context.Context, caller contract.Principal, req contract.RoomHostsRequest) (contract.RoomHostsResponse, *contract.Error) {
	room, problem := s.asker(ctx, caller)
	if problem != nil {
		return contract.RoomHostsResponse{}, problem
	}
	if room != "" && room != req.Room {
		return contract.RoomHostsResponse{}, forbidden()
	}
	if problem := s.checkRoomHosts(ctx, req); problem != nil {
		return contract.RoomHostsResponse{}, problem
	}
	if room != "" {
		op, err := s.approvals.Submit(ctx, caller, contract.Operation{Room: room, Kind: req.OpKind(), Args: req})
		if err != nil {
			return contract.RoomHostsResponse{}, failure(err)
		}
		return contract.RoomHostsResponse{Operation: op.ID}, nil
	}
	if err := s.setRoomHosts(ctx, req); err != nil {
		return contract.RoomHostsResponse{}, failure(err)
	}
	return contract.RoomHostsResponse{}, nil
}

// RoomHostsEntry registers a session's rule.
func (s *Service) RoomHostsEntry() approve.Entry {
	return ownerEntry(contract.RootApprovesHosts, func(req contract.RoomHostsRequest) contract.Card {
		card := contract.Card{Title: "asks to let this room's new boxes use any host", Target: contract.Target{Kind: contract.TargetRoom, Name: string(req.Room)}}
		if len(req.Only) > 0 {
			card.Title, card.Body = "asks to keep this room's new boxes on these hosts", strings.Join(req.Only, ", ")
		}
		return card
	}, s.checkRoomHosts, func(ctx context.Context, req contract.RoomHostsRequest) (string, error) {
		return "Room " + string(req.Room) + "'s rule changed.", s.setRoomHosts(ctx, req)
	})
}

// HostRemove removes a host with nothing on it, or with DiscardBoxes, with its boxes failed as on a host lost for good. Only
// the owner reaches it.
func (s *Service) HostRemove(ctx context.Context, caller contract.Principal, req contract.HostRemoveRequest) (contract.HostRemoveResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.HostRemoveResponse{}, forbidden()
	}
	failed, err := s.reconciler.RemoveHost(ctx, req.Host, req.DiscardBoxes)
	if err != nil {
		problem := failure(err)
		var held *store.HostHeldError
		if errors.As(err, &held) && slices.ContainsFunc(held.Held, func(what string) bool { return strings.HasPrefix(what, "session ") }) {
			problem.Message += "; --discard-boxes fails those boxes and removes the host anyway, and work not pushed is lost"
		}
		return contract.HostRemoveResponse{Failed: failed}, problem
	}
	return contract.HostRemoveResponse{Failed: failed}, nil
}

// PoolSet changes a pool's max, per-session switch or idle time. Only the owner reaches it, and the step reads the pool on its
// next pass.
func (s *Service) PoolSet(ctx context.Context, caller contract.Principal, req contract.PoolSetRequest) (contract.PoolSetResponse, *contract.Error) {
	switch {
	case caller.Kind != contract.PrincipalOwner:
		return contract.PoolSetResponse{}, forbidden()
	case req.Max == nil && req.PerSession == nil && req.Idle == nil:
		return contract.PoolSetResponse{}, invalid("set max, idle or perSession")
	case req.Max != nil && *req.Max < 1:
		return contract.PoolSetResponse{}, invalid(poolMaxTooLow)
	}
	var idle *time.Duration
	if req.Idle != nil {
		if problem := checkIdle(*req.Idle); problem != nil {
			return contract.PoolSetResponse{}, problem
		}
		checked := idleTime(*req.Idle)
		idle = &checked
	}
	err := s.store.SetPool(ctx, req.Name, req.Max, req.PerSession, idle)
	if errors.Is(err, store.ErrNotFound) {
		return contract.PoolSetResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "no pool is named " + req.Name}
	}
	if err != nil {
		return contract.PoolSetResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.PoolSetResponse{}, nil
}
