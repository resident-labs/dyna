package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
	"dyna-agent/internal/websvc"
)

// Worker GitHub commands execute in the engine. Only their policy-checked
// output returns to the worker; credentials never leave the engine.
func (s services) gitHubCommandBinding() websvc.Binding {
	return websvc.Binding{Name: "GitHubCommand", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true, Method: s.gitHubCommand}
}

func (s services) gitPushBinding() websvc.Binding {
	return websvc.Binding{Name: "GitPush", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.gitPush}
}

func (s services) gitHubCommand(ctx context.Context, request ghclient.CommandRequest) (ghclient.CommandResult, error) {
	rec, err := s.workerGitHubRoom(ctx)
	if err != nil {
		return ghclient.CommandResult{}, err
	}
	return ghclient.RunSessionGitHub(ctx, rec, request)
}

func (s services) gitPush(ctx context.Context, request ghclient.CommandRequest) (ghclient.CommandResult, error) {
	rec, err := s.workerGitHubRoom(ctx)
	if err != nil {
		return ghclient.CommandResult{}, err
	}
	return ghclient.RunSessionGitPush(ctx, rec, request)
}

func (s services) workerGitHubRoom(ctx context.Context) (room.Record, error) {
	roomID := authority.Of(ctx).Room
	if roomID == "" {
		return room.Record{}, errors.New("worker GitHub commands require a room session")
	}
	return s.rooms.Notify.Rooms.Resolve(roomID)
}
