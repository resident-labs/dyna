package engine

import (
	"context"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/watch"
)

// The CLI policy (policyCLI) covers the calls that share a caller policy with
// CLI commands. Global operations keep their existing CLI access; room and run
// services check the target room.

func (s services) syncRoom(ctx context.Context, roomRef string) (string, error) {
	rec, err := s.rooms.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return "", err
	}
	if _, err := authority.ActorIn(ctx, rec.ID); err != nil {
		return "", err
	}
	return rec.ID, nil
}

func (s services) syncRefresh(ctx context.Context, roomRef, repo string) (watch.SyncStarted, error) {
	roomID, err := s.syncRoom(ctx, roomRef)
	if err != nil {
		return watch.SyncStarted{}, err
	}
	return s.rooms.PRs.Watcher().SyncRefresh(ctx, roomID, repo)
}

func (s services) gitExport(ctx context.Context, roomRef, repo string) (watch.SyncStarted, error) {
	roomID, err := s.syncRoom(ctx, roomRef)
	if err != nil {
		return watch.SyncStarted{}, err
	}
	return s.rooms.PRs.Watcher().GitExport(ctx, roomID, repo)
}
