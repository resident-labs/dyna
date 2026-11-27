package engine

import (
	"context"
	"slices"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (s *Service) HostList(ctx context.Context, who contract.Principal, req contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	return s.HostListWithLoad(ctx, who, req, nil)
}

// HostListWithLoad adds current host measurements before projecting the caller's inventory.
// Runtime supplies load; callers without live measurements use HostList.
func (s *Service) HostListWithLoad(ctx context.Context, who contract.Principal, _ contract.HostListRequest, load func(context.Context, []contract.HostSummary) error) (contract.HostListResponse, *contract.Error) {
	var room contract.RoomID
	if who.Kind == contract.PrincipalSession {
		var problem *contract.Error
		room, problem = s.asker(ctx, who)
		if problem != nil {
			return contract.HostListResponse{}, problem
		}
	}
	var only []string
	if room != "" {
		var err error
		only, err = s.store.RoomOnly(ctx, room)
		if err != nil {
			return contract.HostListResponse{}, failure(err)
		}
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	shared, err := s.store.RoomShared(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	leftovers, err := s.store.LeftoverCounts(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	sessions, problem := s.readableHostSessions(ctx, who)
	if problem != nil {
		return contract.HostListResponse{}, problem
	}
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	runners := s.reconciler.RunnerCounts()
	services, err := s.hostServices(ctx, room)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	var imageProblems map[contract.HostName][]contract.RunnerImageProblem
	if s.runnerImageProblems != nil {
		imageProblems = s.runnerImageProblems()
	}
	result := contract.HostListResponse{Hosts: []contract.HostSummary{{Name: contract.ControlPlane, Sessions: sessions[contract.ControlPlane], Shared: shared}}}
	if room != "" {
		result.Hosts = nil // the control plane does not place session boxes
	}
	for _, host := range hosts {
		l := box.Reservations(host, boxes, box.Box{})
		reservations := &contract.HostReservations{CPUMilli: int64(l.CPU) * 1000, CPUCap: l.CPUCap, Memory: l.Memory, MemoryCap: l.MemoryCap, Disk: host.DiskBytes - l.DiskFree, DiskCap: max(0, host.DiskBytes-host.DiskFloor)}
		if room != "" {
			if host.ExecOnly || (host.Pool == "" && len(host.Rooms) > 0 && !slices.Contains(host.Rooms, room)) {
				continue
			}
			if host.Box != "" {
				if !slices.ContainsFunc(boxes, func(b box.Box) bool { return b.ID == host.Box && b.Room == room }) {
					continue
				}
			} else if len(only) > 0 {
				name := string(host.Name)
				if host.Pool != "" {
					name = host.Pool
				}
				if !slices.Contains(only, name) {
					continue
				}
			}
		}
		var cards []contract.GPU
		if len(host.GPUs) > 0 {
			cards = slices.Clone(host.GPUs)
		}
		for i := range cards {
			for _, b := range boxes {
				if b.Host == host.Name && b.GPUCard == cards[i].Index && b.HoldsGPU() {
					cards[i].RequestedBytes += int64(b.GPU) * box.GiB
				}
			}
			cards[i].FreeBytes = max(0, box.GPUUsable(cards[i])-cards[i].RequestedBytes)
		}
		state := string(host.State)
		if host.NoAgents {
			state = "agents off"
		} else if host.Health == contract.HostDegraded {
			state = "degraded"
		}
		result.Hosts = append(result.Hosts, contract.HostSummary{Reservations: reservations, State: state, CPUFree: float64(host.CPUs), MemFree: max(0, host.MemBytes-host.MemUsed), DiskFree: host.DiskBytes, Health: host.Health, DiskBytes: host.DiskTotal, DiskUsed: max(0, host.DiskTotal-host.DiskBytes), Name: host.Name, Problem: host.Problem, Sessions: sessions[host.Name], Shared: []contract.RoomShared{}, Pool: host.Pool, Agents: !host.NoAgents, CI: host.CI, CIScale: host.CIScale, Rooms: host.Rooms, ExecOnly: host.ExecOnly, Agy: host.Agy, Runners: runners[host.Name], RunnerImages: imageProblems[host.Name], CPUs: host.CPUs, MemBytes: host.MemBytes, DiskLow: !host.ExecOnly && !host.CheckedAt.IsZero() && box.DiskLow(host.DiskBytes, host.DiskFloor), EngineHost: host.EngineHost != nil && *host.EngineHost, Leftovers: leftovers[host.Name], GPUs: cards, Services: services[host.Name], ServiceHost: s.reconciler.ServiceHostStatus(host.Name)})
	}
	if load != nil {
		if err := load(ctx, result.Hosts); err != nil {
			return contract.HostListResponse{}, failure(err)
		}
	}
	if room != "" {
		for i, host := range result.Hosts {
			// Session inventory is an allowlist, applied after live measurements so
			// enrichment cannot restore owner-only history, counts or identities.
			entry := contract.HostSummary{
				Name: host.Name, State: host.State, Health: host.Health, Reservations: host.Reservations,
				DiskBytes: host.DiskBytes, DiskUsed: host.DiskUsed,
				Agents: host.Agents, CI: host.CI,
				CPUFree: host.CPUFree, MemFree: host.MemFree, DiskFree: host.DiskFree,
				Services: host.Services,
			}
			for _, card := range host.GPUs {
				entry.GPUs = append(entry.GPUs, contract.GPU{Index: card.Index, Name: card.Name, MemBytes: card.MemBytes, FreeBytes: card.FreeBytes})
			}
			for _, problem := range host.RunnerImages {
				if problem.Room == room {
					entry.RunnerImages = append(entry.RunnerImages, problem)
				}
			}
			result.Hosts[i] = entry
		}
	}
	return result, nil
}

// readableHostSessions keeps each host's sessions in the rooms the caller can read, by the rule session.list applies.
func (s *Service) readableHostSessions(ctx context.Context, who contract.Principal) (map[contract.HostName][]contract.HostSession, *contract.Error) {
	all, err := s.store.HostSessions(ctx)
	if err != nil {
		return nil, failure(err)
	}
	readable := make(map[contract.RoomID]bool)
	out := make(map[contract.HostName][]contract.HostSession)
	for host, sessions := range all {
		for _, session := range sessions {
			can, checked := readable[session.Room]
			if !checked {
				problem := s.roomAccess(ctx, who, session.Room)
				if problem != nil && problem.Code != contract.ErrForbidden {
					return nil, problem
				}
				can = problem == nil
				readable[session.Room] = can
			}
			if can {
				out[host] = append(out[host], session)
			}
		}
	}
	return out, nil
}

// Terminal prepares a target and wakes its box.
func (s *Service) Terminal(ctx context.Context, session contract.SessionID, host contract.HostName) ([]string, func(), *contract.Error) {
	var id contract.BoxID
	release := func() {}
	if session != "" {
		var problem *contract.Error
		if host, id, release, problem = s.holdBox(ctx, session); problem != nil {
			return nil, release, problem
		}
	}
	argv, err := s.boxes.TerminalArgv(ctx, host, id)
	if err != nil {
		release()
		return nil, func() {}, failure(err)
	}
	return argv, release, nil
}

// readyBox returns the box of an open session, if it is ready.
func (s *Service) readyBox(ctx context.Context, session contract.SessionID) (box.Box, *contract.Error) {
	record, open, err := s.store.OpenSession(ctx, session)
	if err != nil {
		return box.Box{}, failure(err)
	}
	if !open {
		return box.Box{}, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	current, err := s.store.Box(ctx, record.Box)
	if err != nil {
		return box.Box{}, failure(err)
	}
	if current.State != contract.BoxReady {
		return box.Box{}, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
	}
	return current, nil
}

// holdBox keeps a session's ready box awake until release, for an owner terminal or computer view, and wakes a box that
// was about to stop.
func (s *Service) holdBox(ctx context.Context, session contract.SessionID) (contract.HostName, contract.BoxID, func(), *contract.Error) {
	release := func() {}
	current, problem := s.readyBox(ctx, session)
	if problem != nil {
		return "", "", release, problem
	}
	handle, problem := s.createSession(ctx, session)
	if problem != nil {
		return "", "", release, problem
	}
	if !handle.OpenTerminal() {
		return "", "", release, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	release = handle.CloseTerminal
	if err := s.store.WakeTerminal(ctx, current.ID); err != nil {
		release()
		return "", "", func() {}, failure(err)
	}
	s.reconciler.Kick()
	return current.Host, current.ID, release, nil
}
