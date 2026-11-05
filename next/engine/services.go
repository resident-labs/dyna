package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

// serviceFailure maps what the service store and reconciler refuse to the typed codes of the wire.
func serviceFailure(err error) *contract.Error {
	var taken *store.PortTakenError
	switch {
	case errors.As(err, &taken):
		return &contract.Error{Code: contract.ErrPortTaken, Message: err.Error()}
	case errors.Is(err, store.ErrServiceHostFixed), errors.Is(err, lifecycle.ErrNoPrevious), errors.Is(err, lifecycle.ErrNotPlaced), errors.Is(err, lifecycle.ErrServiceEngineHost), errors.Is(err, lifecycle.ErrServiceRemoving):
		return &contract.Error{Code: contract.ErrConflict, Message: err.Error()}
	}
	return failure(err)
}

// serviceNamed finds a room's service by name.
func (s *Service) serviceNamed(ctx context.Context, ref contract.ServiceRef) (store.ServiceRow, *contract.Error) {
	row, err := s.store.ServiceNamed(ctx, ref.Room, ref.Name)
	if err != nil {
		return row, serviceFailure(err)
	}
	return row, nil
}

func (s *Service) ServiceSet(ctx context.Context, who contract.Principal, req contract.ServiceSetRequest) (contract.ServiceSetResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceSetResponse{}, forbidden()
	}
	parts, err := req.Spec.Parse()
	if err != nil {
		return contract.ServiceSetResponse{}, invalid(err.Error())
	}
	unlock, err := s.reconciler.LockServiceDeclaration(ctx, req.Room, parts.Spec.Name)
	if err != nil {
		return contract.ServiceSetResponse{}, failure(err)
	}
	defer unlock()
	limits, err := s.store.RoomLimits(ctx, req.Room)
	if err != nil {
		return contract.ServiceSetResponse{}, failure(err)
	}
	hosts, err := s.reconciler.AdmissionHosts(ctx)
	if err != nil {
		return contract.ServiceSetResponse{}, failure(err)
	}
	if parts.Spec.Host != "" {
		// A declared host is one of the room's: it exists and the room's hosts rule allows it.
		only, err := s.store.RoomOnly(ctx, req.Room)
		if err != nil {
			return contract.ServiceSetResponse{}, failure(err)
		}
		if !slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == parts.Spec.Host }) {
			return contract.ServiceSetResponse{}, invalid("no host is named " + string(parts.Spec.Host))
		}
		if len(only) > 0 && !slices.Contains(only, string(parts.Spec.Host)) {
			return contract.ServiceSetResponse{}, invalid("host " + string(parts.Spec.Host) + " is outside the room's hosts rule")
		}
	}
	// A host port below the host's lowest unprivileged port is refused when the host's last check is known.
	host := parts.Spec.Host
	if existing, err := s.store.ServiceNamed(ctx, req.Room, parts.Spec.Name); err == nil && existing.Host != "" {
		host = existing.Host
		// A change of a placed service is judged by admission as a new placement is, with the service's own reservation set aside.
		boxes, err := s.store.Boxes(ctx)
		if err != nil {
			return contract.ServiceSetResponse{}, failure(err)
		}
		boxes = append(boxes, s.reconciler.RunnerBoxes()...)
		candidate := existing
		candidate.Parts = parts
		fitted := candidate.Reservation()
		fitted.Spec.Host = existing.Host
		holds, err := s.store.ServiceHolds(ctx)
		if err != nil {
			return contract.ServiceSetResponse{}, failure(err)
		}
		estimate, err := s.store.ImageEstimate(ctx, req.Room, parts)
		if err != nil {
			return contract.ServiceSetResponse{}, failure(err)
		}
		request := box.ServiceRequest{ID: existing.ID, Room: req.Room, Parts: fitted, ImageEstimate: max(existing.ImageBytes, estimate)}
		if !box.ServiceFitsRoom(request, limits, boxes, holds) {
			return contract.ServiceSetResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "the change exceeds the room's CPU or memory limit"}
		}
		if p := box.AdmitService(hosts, boxes, request); p.Host == "" {
			return contract.ServiceSetResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("the change does not fit host %s (%s)", existing.Host, p.Reason)}
		}
	}
	if status := s.reconciler.ServiceHostStatus(host); status != nil {
		for _, port := range parts.Ports {
			if port.Host < status.LowestPort {
				return contract.ServiceSetResponse{}, &contract.Error{Code: contract.ErrPortPrivileged, Message: "host port is below the host's net.ipv4.ip_unprivileged_port_start"}
			}
		}
	}
	value, err := id("svc_", s.now())
	if err != nil {
		return contract.ServiceSetResponse{}, failure(err)
	}
	row, err := s.store.SetService(ctx, req.Room, parts, contract.ServiceID(value), who, s.now(), func(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
		return s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, body)
	})
	if err != nil {
		return contract.ServiceSetResponse{}, serviceFailure(err)
	}
	s.reconciler.Kick()
	return contract.ServiceSetResponse{Service: row.Service}, nil
}

func (s *Service) ServiceList(ctx context.Context, who contract.Principal, req contract.ServiceListRequest) (contract.ServiceListResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceListResponse{}, forbidden()
	}
	rows, err := s.store.Services(ctx, req.Room)
	if err != nil {
		return contract.ServiceListResponse{}, failure(err)
	}
	result := contract.ServiceListResponse{Services: []contract.Service{}}
	for _, row := range rows {
		result.Services = append(result.Services, row.Service)
	}
	return result, nil
}

func (s *Service) ServiceShow(ctx context.Context, who contract.Principal, req contract.ServiceShowRequest) (contract.ServiceShowResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceShowResponse{}, forbidden()
	}
	row, problem := s.serviceNamed(ctx, req)
	return contract.ServiceShowResponse{Service: row.Service}, problem
}

func (s *Service) ServiceLogs(ctx context.Context, who contract.Principal, req contract.ServiceLogsRequest) (contract.ServiceLogsResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceLogsResponse{}, forbidden()
	}
	lines := req.Lines
	if lines == 0 {
		lines = contract.ServiceLogsDefault
	}
	if lines < 1 || lines > contract.ServiceLogsMax {
		return contract.ServiceLogsResponse{}, invalid("lines is 1 to 2000")
	}
	since, err := req.Since.Parse()
	if err != nil {
		return contract.ServiceLogsResponse{}, invalid("since is a duration such as 10m")
	}
	row, problem := s.serviceNamed(ctx, contract.ServiceRef{Room: req.Room, Name: req.Name})
	if problem != nil {
		return contract.ServiceLogsResponse{}, problem
	}
	text, err := s.reconciler.ServiceLogs(ctx, row, lines, since)
	return contract.ServiceLogsResponse{Text: text}, serviceFailureOrNil(err)
}

func serviceFailureOrNil(err error) *contract.Error {
	if err == nil {
		return nil
	}
	return serviceFailure(err)
}

// serviceAction runs one of the changes of a service's wants on the service a ref names.
func (s *Service) serviceAction(ctx context.Context, ref contract.ServiceRef, act func(context.Context, store.ServiceRow) (store.ServiceRow, error)) (contract.ServiceActionResponse, *contract.Error) {
	row, problem := s.serviceNamed(ctx, ref)
	if problem != nil {
		return contract.ServiceActionResponse{}, problem
	}
	row, err := act(ctx, row)
	return contract.ServiceActionResponse{Service: row.Service}, serviceFailureOrNil(err)
}

func (s *Service) ServiceRestart(ctx context.Context, who contract.Principal, req contract.ServiceRestartRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, s.reconciler.RestartServiceUnit)
}

func (s *Service) ServiceStop(ctx context.Context, who contract.Principal, req contract.ServiceStopRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, func(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
		return s.reconciler.StopService(ctx, row.ID)
	})
}

func (s *Service) ServiceStart(ctx context.Context, who contract.Principal, req contract.ServiceStartRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, func(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
		return s.reconciler.StartService(ctx, row.ID)
	})
}

func (s *Service) ServiceUpdate(ctx context.Context, who contract.Principal, req contract.ServiceUpdateRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, func(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
		return s.reconciler.UpdateService(ctx, row.ID)
	})
}

func (s *Service) ServiceRollback(ctx context.Context, who contract.Principal, req contract.ServiceRollbackRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, func(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
		return s.reconciler.RollbackService(ctx, row.ID)
	})
}

func (s *Service) ServiceRemove(ctx context.Context, who contract.Principal, req contract.ServiceRemoveRequest) (contract.ServiceActionResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceActionResponse{}, forbidden()
	}
	return s.serviceAction(ctx, req, func(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
		return row, s.reconciler.RemoveService(ctx, row.ID)
	})
}

// ServiceSecretSet writes a secret's value. The response holds nothing of it, and nothing reads it back.
func (s *Service) ServiceSecretSet(ctx context.Context, who contract.Principal, req contract.ServiceSecretSetRequest) (contract.ServiceSecretSetResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ServiceSecretSetResponse{}, forbidden()
	}
	unlock, err := s.reconciler.LockServiceDeclaration(ctx, req.Room, req.Name)
	if err != nil {
		return contract.ServiceSecretSetResponse{}, failure(err)
	}
	defer unlock()
	row, problem := s.serviceNamed(ctx, contract.ServiceRef{Room: req.Room, Name: req.Name})
	if problem != nil {
		return contract.ServiceSecretSetResponse{}, problem
	}
	if req.Value == "" {
		return contract.ServiceSecretSetResponse{}, invalid("a secret's value is not empty")
	}
	if err := s.store.SetServiceSecret(ctx, row.ID, req.Secret, []byte(req.Value), time.Now()); err != nil {
		return contract.ServiceSecretSetResponse{}, serviceFailure(err)
	}
	s.reconciler.Kick()
	return contract.ServiceSecretSetResponse{}, nil
}

// hostServices lists each host's services for host.list: the room, name, state and what each reserves.
func (s *Service) hostServices(ctx context.Context) (map[contract.HostName][]contract.HostService, error) {
	rows, err := s.store.Services(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[contract.HostName][]contract.HostService{}
	for _, row := range rows {
		if row.Host != "" {
			out[row.Host] = append(out[row.Host], contract.HostService{Room: row.Room, Name: row.Spec.Name, State: row.State, CPUMilli: row.CPUMilli, MemBytes: row.MemBytes})
		}
	}
	return out, nil
}
