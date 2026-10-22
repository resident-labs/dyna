package roomsvc

import (
	"context"

	"dyna-agent/internal/subagent"
)

func (s *Service) TransferSession(ctx context.Context, roomRef, ref string, opts subagent.TransferOptions) (subagent.TransferState, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return subagent.TransferState{}, err
	}
	return s.Notify.Sessions.TransferContext(ctx, info.ID, opts)
}

func (s *Service) SessionTransfer(roomRef, ref string) (*subagent.TransferState, error) {
	info, err := s.SessionInRoom(roomRef, ref)
	if err != nil {
		return nil, err
	}
	return s.Notify.Sessions.TransferStatus(info.ID)
}

func (s *Service) CancelSessionTransfer(ctx context.Context, roomRef, ref string) (subagent.TransferState, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return subagent.TransferState{}, err
	}
	return s.Notify.Sessions.CancelTransfer(ctx, info.ID)
}

// SubmitTransferHandoff records the handoff that a transfer asked session
// ref for. The token names the transfer, so a stale handoff cannot satisfy
// a newer one.
func (s *Service) SubmitTransferHandoff(ctx context.Context, roomRef, ref, token, text, dir, branch string) error {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return err
	}
	return s.Notify.Sessions.SubmitHandoff(ctx, info.ID, token, text, dir, branch)
}
