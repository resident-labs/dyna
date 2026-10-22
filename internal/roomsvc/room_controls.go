package roomsvc

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func (s *Service) RoomSessionControls(roomID string) (subagent.RoomSessionState, error) {
	return s.Notify.Sessions.RoomSessionControls(roomID)
}
func (s *Service) PauseRoomSessions(ctx context.Context, roomID string) (subagent.RoomSessionMutation, error) {
	return s.Notify.Sessions.PauseRoomSessions(ctx, roomID)
}
func (s *Service) ResumeRoomSessions(ctx context.Context, roomID string) (subagent.RoomSessionMutation, error) {
	return s.Notify.Sessions.ResumeRoomSessions(ctx, roomID)
}
func (s *Service) ClearDormantSessions(ctx context.Context, roomID string) (subagent.RoomSessionMutation, error) {
	return s.Notify.Sessions.ClearDormantSessions(ctx, roomID)
}

// DismissSession closes one finished session for the owner, unless a message
// woke it first.
func (s *Service) DismissSession(ctx context.Context, roomRef, ref string) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.DismissSession(ctx, info.ID); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}

// AttachSeat gives the room's root seat to an external orchestrator, for
// `dyna root attach`.
func (s *Service) AttachSeat(ctx context.Context, roomID, harness string) (notify.SeatAttach, error) {
	if err := authority.Check("root attach", authority.Of(ctx), nil); err != nil {
		return notify.SeatAttach{}, err
	}
	rec, err := s.rooms().Resolve(roomID)
	if err != nil {
		return notify.SeatAttach{}, err
	}
	return s.Notify.AttachSeat(ctx, rec, harness)
}

// SeatReturn is what giving the root seat back did.
type SeatReturn struct {
	Seat    room.Seat `json:"seat"`
	Changed bool      `json:"changed"`
	// Deliveries are the handoff's deliveries to the internal root.
	Deliveries []notify.Delivery `json:"deliveries,omitempty"`
}

// ReturnSeat gives the room's root seat back to the internal root, for
// `dyna root detach` and for an owner whose external orchestrator stopped.
// A handoff becomes the internal root's next input and wakes it.
func (s *Service) ReturnSeat(ctx context.Context, roomID, handoff string) (SeatReturn, error) {
	if err := authority.Check("root detach", authority.Of(ctx), nil); err != nil {
		return SeatReturn{}, err
	}
	rec, err := s.rooms().Resolve(roomID)
	if err != nil {
		return SeatReturn{}, err
	}
	seat, changed, deliveries, err := s.Notify.ReturnSeat(ctx, rec, handoff)
	return SeatReturn{Seat: seat, Changed: changed, Deliveries: deliveries}, err
}
