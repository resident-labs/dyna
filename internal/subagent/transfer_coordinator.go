package subagent

import (
	"context"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
)

// transferCoordinator owns the transfer state machine. The callbacks keep its
// dependencies on the session manager explicit without exporting store internals.
type transferCoordinator struct {
	store                  func() *Store
	RoomPaused             func(string) bool
	rooms                  func() *room.Store
	profiles               func() (*profile.Store, error)
	transferNow            func() time.Time
	transferSleep          func(context.Context, time.Duration) error
	launch                 func(string, string) error
	waitRoom               func(context.Context, string) error
	coordinatorPauseBudget func(context.Context, string, time.Time) (time.Duration, error)
	stopSession            func(context.Context, string, string, string, string) (Turn, error)
	stopHarnessProcesses   func(context.Context, string) error
	convertPiToClaude      func(context.Context, Meta, TransferState, profile.Profile) (string, error)
	handoffPacket          func(Meta, TransferState) (string, string, error)
	postFallbackNotice     func(string, string)
	transferCompactionSeen func(string, Turn) bool
	beginTurnConfigured    func(string, string, string, []string, string, string, bool, bool, time.Time) (Turn, bool, error)
}

func (m *Manager) transferCoordinator() *transferCoordinator {
	return &transferCoordinator{
		store: m.store, RoomPaused: m.RoomPaused, rooms: m.rooms, profiles: m.profiles,
		transferNow: m.transferNow, transferSleep: m.transferSleep,
		launch: func(id, token string) error {
			launch := m.TransferLaunch
			if launch == nil {
				launch = goTransfer
			}
			return launch(m, id, token)
		},
		waitRoom: m.waitRoom, coordinatorPauseBudget: m.coordinatorPauseBudget,
		stopSession: m.stopSession, stopHarnessProcesses: m.stopHarnessProcesses,
		convertPiToClaude: m.convertPiToClaude, handoffPacket: m.handoffPacket,
		postFallbackNotice: m.postFallbackNotice, transferCompactionSeen: m.transferCompactionSeen,
		beginTurnConfigured: m.beginTurnConfigured,
	}
}
func (m *Manager) requestTransfer(ctx context.Context, ref string, opts TransferOptions) (TransferState, error) {
	return m.transferCoordinator().requestTransfer(ctx, ref, opts)
}
func (m *Manager) TransferStatus(ref string) (*TransferState, error) {
	return m.transferCoordinator().TransferStatus(ref)
}
func (m *Manager) updateTransfer(id, token string, fn func(*Meta, *TransferState) error) error {
	return m.transferCoordinator().updateTransfer(id, token, fn)
}
func (m *Manager) SubmitHandoff(ctx context.Context, ref, token, text, dir, branch string) error {
	return m.transferCoordinator().SubmitHandoff(ctx, ref, token, text, dir, branch)
}
func (m *Manager) CancelTransfer(ctx context.Context, ref string) (TransferState, error) {
	return m.transferCoordinator().CancelTransfer(ctx, ref)
}
func (m *Manager) runTransfer(ctx context.Context, id, token string) error {
	return m.transferCoordinator().runTransfer(ctx, id, token)
}
func (m *Manager) advanceTransfer(ctx context.Context, id, token string) (bool, error) {
	return m.transferCoordinator().advanceTransfer(ctx, id, token)
}
