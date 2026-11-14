package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/engine/discord"
)

// Rebuild from Discord before RESUME: the gateway will not resend GUILD_CREATE,
// and persisted metadata would miss changes made while the engine was down.
func (s *Service) discordRestoreDirectory(ctx context.Context, client *discord.Client) error {
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		return err
	}
	guilds := map[string]bool{}
	for _, route := range routes {
		if guilds[route.Guild] {
			continue
		}
		guilds[route.Guild] = true
		var server discord.Server
		var threads struct {
			Threads []discord.Channel `json:"threads"`
		}
		for _, request := range []struct {
			path   string
			target any
		}{
			{"/api/v10/guilds/" + route.Guild, &server},
			{"/api/v10/guilds/" + route.Guild + "/channels", &server.Channels},
			{"/api/v10/guilds/" + route.Guild + "/threads/active", &threads},
		} {
			result, err := client.API(ctx, "GET", request.path, nil, "")
			if err != nil {
				return err
			}
			if result.Status != 200 {
				return fmt.Errorf("Discord directory %s returned %d", request.path, result.Status)
			}
			if err := json.Unmarshal(result.Body, request.target); err != nil {
				return err
			}
		}
		server.ID = route.Guild
		server.Threads = threads.Threads
		data, err := json.Marshal(server)
		if err != nil {
			return err
		}
		s.discordDirectory.Apply("GUILD_CREATE", data)
	}
	return nil
}
