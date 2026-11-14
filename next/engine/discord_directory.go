package engine

import (
	"context"
	"encoding/json"
	"log"

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
guildLoop:
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
				log.Printf("Discord directory guild %s: %v", route.Guild, err)
				continue guildLoop
			}
			if result.Status != 200 {
				log.Printf("Discord directory guild %s: %s returned %d", route.Guild, request.path, result.Status)
				continue guildLoop
			}
			if err := json.Unmarshal(result.Body, request.target); err != nil {
				log.Printf("Discord directory guild %s: %v", route.Guild, err)
				continue guildLoop
			}
		}
		server.ID = route.Guild
		server.Threads = threads.Threads
		data, err := json.Marshal(server)
		if err != nil {
			log.Printf("Discord directory guild %s: %v", route.Guild, err)
			continue
		}
		s.discordDirectory.Apply("GUILD_CREATE", data)
	}
	return nil
}
