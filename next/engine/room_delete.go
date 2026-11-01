package engine

import (
	"context"
	"os"

	"github.com/resident-labs/dyna/contract"
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
	response, err := s.store.MarkRoomDeleting(ctx, req.Room, event)
	if err != nil {
		return contract.RoomDeleteResponse{}, failure(err)
	}
	_ = s.StopHostexecs(ctx) // The intent is durable, and the engine's sweep retries a stop that fails here.
	s.reconciler.Kick()
	return response, nil
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
	return os.RemoveAll(dir)
}

// RoomDeleted tells the owner's stream that the room is gone. No row can hold this event, so it goes out live.
func (s *Service) RoomDeleted(room contract.RoomID) {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.RoomDeleted{})
	if err == nil {
		s.live.Publish(room, event, true)
	}
}
