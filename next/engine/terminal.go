package engine

import (
	"context"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) HostList(ctx context.Context, who contract.Principal, _ contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	shared, err := s.store.RoomShared(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	status, err := s.store.BackupCopyStatus(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	var copyFailure contract.BackupCopyFailure
	if status.Failure != "" {
		copyFailure = contract.BackupCopyFailed
		if status.Failure == store.BackupCopyNoSpace {
			copyFailure = contract.BackupCopyNoSpace
		}
	}
	leftovers, err := s.store.LeftoverCounts(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	result := contract.HostListResponse{Hosts: []contract.HostSummary{{Name: contract.ControlPlane, Shared: []contract.RoomShared{}, BackupCopyAt: status.LastSuccess, BackupCopyFailure: copyFailure}}}
	for _, host := range hosts {
		result.Hosts = append(result.Hosts, contract.HostSummary{Name: host.Name, Shared: append([]contract.RoomShared{}, shared[host.Name]...), CPUs: host.CPUs, MemBytes: host.MemBytes, DiskLow: !host.CheckedAt.IsZero() && host.DiskBytes < box.DiskWarn(host.DiskFloor), Leftovers: leftovers[host.Name]})
	}
	return result, nil
}

// Terminal prepares a target and wakes its box.
func (s *Service) Terminal(ctx context.Context, session contract.SessionID, host contract.HostName) ([]string, func(), *contract.Error) {
	var id contract.BoxID
	release := func() {}
	if session != "" {
		record, open, err := s.store.OpenSession(ctx, session)
		if err != nil {
			return nil, release, failure(err)
		}
		if !open {
			return nil, release, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
		}
		current, err := s.store.Box(ctx, record.Box)
		if err != nil {
			return nil, release, failure(err)
		}
		if current.State != contract.BoxReady {
			return nil, release, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
		}
		host, id = current.Host, current.ID
		handle, problem := s.createSession(ctx, session)
		if problem != nil {
			return nil, release, problem
		}
		if !handle.OpenTerminal() {
			return nil, release, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
		}
		release = handle.CloseTerminal
		if err := s.store.WakeTerminal(ctx, id); err != nil {
			release()
			return nil, func() {}, failure(err)
		}
		s.reconciler.Kick()
	}
	argv, err := s.boxes.TerminalArgv(ctx, host, id)
	if err != nil {
		release()
		return nil, func() {}, failure(err)
	}
	return argv, release, nil
}
