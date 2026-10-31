package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"time"
)

func deviceOwner(c contract.Principal) *contract.Error {
	if c.Kind != contract.PrincipalOwner {
		return forbidden()
	}
	return nil
}

func validScopes(scopes []contract.Scope) bool {
	seen := map[contract.Scope]bool{}
	for _, scope := range scopes {
		switch scope {
		case contract.ScopeRead, contract.ScopeOperate, contract.ScopeFiles, contract.ScopeTerminal, contract.ScopeComputers, contract.ScopeAdmin:
		default:
			return false
		}
		if seen[scope] {
			return false
		}
		seen[scope] = true
	}
	return true
}

func (s *Service) DevicePair(ctx context.Context, caller contract.Principal, req contract.DevicePairRequest) (contract.DevicePairResponse, *contract.Error) {
	var out contract.DevicePairResponse
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	if !validScopes(req.Scopes) {
		return out, invalid("invalid or duplicate scope")
	}
	now := s.now()
	if len(req.Scopes) == 0 {
		req.Scopes = nil
	}
	code, err := s.store.NewPairCode(ctx, req.Scopes, now)
	if err != nil {
		return out, failure(err)
	}
	return contract.DevicePairResponse{Code: code, ExpiresAt: now.Add(5 * time.Minute)}, nil
}
func (s *Service) DeviceList(ctx context.Context, caller contract.Principal, _ contract.DeviceListRequest) (contract.DeviceListResponse, *contract.Error) {
	out := contract.DeviceListResponse{Devices: []contract.Device{}}
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	devices, err := s.store.ListDevices(ctx, s.now())
	if err != nil {
		return out, failure(err)
	}
	for _, d := range devices {
		out.Devices = append(out.Devices, contract.Device{ID: d.ID, Name: d.Name, PairedAt: d.PairedAt, LastSeenAt: d.LastSeenAt, ExpiresAt: d.ExpiresAt, Scopes: d.Scopes})
	}
	return out, nil
}
func (s *Service) DeviceRevoke(ctx context.Context, caller contract.Principal, req contract.DeviceRevokeRequest) (contract.DeviceRevokeResponse, *contract.Error) {
	var out contract.DeviceRevokeResponse
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	found, err := s.store.RevokeDevice(ctx, req.ID, s.now())
	if err != nil {
		return out, failure(err)
	}
	if !found {
		return out, invalid("device not found")
	}
	return out, nil
}
func (s *Service) DeviceSetScopes(ctx context.Context, caller contract.Principal, req contract.DeviceSetScopesRequest) (contract.DeviceSetScopesResponse, *contract.Error) {
	var out contract.DeviceSetScopesResponse
	if problem := deviceOwner(caller); problem != nil {
		return out, problem
	}
	if !validScopes(req.Scopes) {
		return out, invalid("invalid or duplicate scope")
	}
	found, err := s.store.SetDeviceScopes(ctx, req.ID, req.Scopes)
	if err != nil {
		return out, failure(err)
	}
	if !found {
		return out, invalid("device not found")
	}
	return out, nil
}
