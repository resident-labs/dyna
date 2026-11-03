package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// RoomDelete records the intent; the reconciler does the rest and resumes it after a restart (5.9).
func (s *Service) RoomDelete(ctx context.Context, caller contract.Principal, req contract.RoomDeleteRequest) (contract.RoomDeleteResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomDeleteResponse{}, forbidden()
	}
	if !req.Discard {
		return contract.RoomDeleteResponse{}, invalid("deleting a room discards all its work, so it needs discard")
	}
	event, err := s.event(req.Room, caller, contract.RoomDeleting{})
	if err != nil {
		return contract.RoomDeleteResponse{}, failure(err)
	}
	var limited *store.RoomLimitedError
	err = s.store.MarkRoomDeleting(ctx, req.Room, event)
	if errors.As(err, &limited) {
		return contract.RoomDeleteResponse{}, &contract.Error{Code: contract.ErrRoomLimited, Message: limited.Error(), Limited: &limited.Limited}
	}
	if err != nil {
		return contract.RoomDeleteResponse{}, failure(err)
	}
	_ = s.StopHostexecs(ctx) // The intent is durable, and the engine's sweep retries a stop that fails here.
	s.reconciler.Kick()
	return contract.RoomDeleteResponse{}, nil
}

// HostSetLimits replaces a host's room limit, which is how the owner clears a room_limited refusal for a host.
func (s *Service) HostSetLimits(ctx context.Context, caller contract.Principal, req contract.HostSetLimitsRequest) (contract.HostSetLimitsResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.HostSetLimitsResponse{}, forbidden()
	}
	if err := s.store.SetHostRooms(ctx, req.Host, req.Rooms); err != nil {
		return contract.HostSetLimitsResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.HostSetLimitsResponse{}, nil
}

// HostSet turns a host's agents and CI switches. Admission and the CI pool read them on their next pass.
func (s *Service) HostSet(ctx context.Context, caller contract.Principal, req contract.HostSetRequest) (contract.HostSetResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.HostSetResponse{}, forbidden()
	}
	if req.Agents == nil && req.CI == nil && !req.ClearCreateProblem {
		return contract.HostSetResponse{}, invalid("set agents, ci or clear the create problem")
	}
	if err := s.store.SetHostSwitches(ctx, req.Host, req.Agents, req.CI, req.ClearCreateProblem); err != nil {
		return contract.HostSetResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.HostSetResponse{}, nil
}

// RoomSet changes a room's settings. Only the owner does, and an unset one stays.
func (s *Service) RoomSet(ctx context.Context, caller contract.Principal, req contract.RoomSetRequest) (contract.RoomSetResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomSetResponse{}, forbidden()
	}
	if req.RootApprovesHostexec == nil {
		return contract.RoomSetResponse{}, invalid("set rootApprovesHostexec")
	}
	if err := s.store.SetRoomRootApprovesHostexec(ctx, req.Room, *req.RootApprovesHostexec); err != nil {
		return contract.RoomSetResponse{}, failure(err)
	}
	return contract.RoomSetResponse{}, nil
}

// roomWrite runs a write to the room's folder while no deletion can remove it, and refuses a room that is going or gone.
// So nothing recreates a folder after the deletion removed it.
func (s *Service) roomWrite(ctx context.Context, room contract.RoomID, write func() error) error {
	s.roomData.RLock()
	defer s.roomData.RUnlock()
	if err := s.store.RoomOpen(ctx, room); err != nil {
		return err
	}
	return write()
}

// RemoveRoomData removes the room's folder on the control plane, after waiting for writes that passed roomWrite.
func (s *Service) RemoveRoomData(room contract.RoomID) error {
	dir, err := s.layout.Room(room)
	if err != nil {
		return err
	}
	s.roomData.Lock()
	defer s.roomData.Unlock()
	err = os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	// A box can leave read-only folders and files under a subordinate ID in its shared folder. Only the user namespace can remove those.
	if out, unshareErr := exec.Command("podman", "unshare", "rm", "-rf", "--", dir).CombinedOutput(); unshareErr != nil {
		return fmt.Errorf("%w: %v: %s", err, unshareErr, out)
	}
	return nil
}

// RoomDeleted tells the owner's stream that the room is gone. No row can hold this event, so it goes out live.
func (s *Service) RoomDeleted(room contract.RoomID) {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.RoomDeleted{})
	if err == nil {
		s.live.Publish(room, event, true)
	}
}
