package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) HostList(ctx context.Context, who contract.Principal, _ contract.HostListRequest) (contract.HostListResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.HostListResponse{}, forbidden()
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return contract.HostListResponse{}, failure(err)
	}
	result := contract.HostListResponse{Hosts: []contract.HostSummary{{Name: contract.ControlPlane}}}
	for _, host := range hosts {
		result.Hosts = append(result.Hosts, contract.HostSummary{Name: host.Name, CPUs: host.CPUs, MemBytes: host.MemBytes})
	}
	return result, nil
}

// Terminal prepares a target while holding its box lock against dormancy.
func (s *Service) Terminal(ctx context.Context, session contract.SessionID, host contract.HostName) ([]string, func(), *contract.Error) {
	var id contract.BoxID
	release := func() {}
	if session != "" {
		unlock := s.lockBox(session)
		defer unlock()
		record, err := s.store.Session(ctx, session)
		if err != nil {
			return nil, release, failure(err)
		}
		current, err := s.store.Box(ctx, record.Box)
		if err != nil {
			return nil, release, failure(err)
		}
		if current.State != contract.BoxReady {
			return nil, release, &contract.Error{Code: contract.ErrConflict, Message: "box is not ready"}
		}
		host, id = current.Host, current.ID
		s.mu.Lock()
		s.sessionLocked(session).terminals++
		s.mu.Unlock()
		release = func() {
			s.mu.Lock()
			s.sessions[session].terminals--
			s.mu.Unlock()
		}
	}
	argv, err := s.boxes.TerminalArgv(ctx, host, id)
	if err != nil {
		release()
		return nil, func() {}, failure(err)
	}
	return argv, release, nil
}
