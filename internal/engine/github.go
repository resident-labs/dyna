package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/websvc"
)

// gitHubCredentialBinding keeps account selection in the engine. Only local
// session processes can call it, and verified caller identity chooses the room.
func (s services) gitHubCredentialBinding() websvc.Binding {
	return websvc.Binding{Name: "GitHubCredential", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true, Method: s.gitHubCredential}
}

func (s services) gitHubCredential(ctx context.Context, host, name string) (ghclient.Credential, error) {
	roomID := authority.Of(ctx).Room
	if roomID == "" {
		return ghclient.Credential{}, errors.New("GitHub credentials require a room session")
	}
	rec, err := s.rooms.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return ghclient.Credential{}, err
	}
	return ghclient.RoomCredential(ctx, rec, host, name)
}
