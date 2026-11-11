package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/credential"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) RoomDiscord(ctx context.Context, caller contract.Principal, req contract.RoomDiscordRequest) (contract.RoomDiscordResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomDiscordResponse{}, forbidden()
	}
	if s.discordTokens == nil {
		return contract.RoomDiscordResponse{}, invalid("Discord unavailable")
	}
	room, _, err := s.store.ResolveRoom(ctx, string(req.Room))
	if err != nil {
		return contract.RoomDiscordResponse{}, invalid("unknown room")
	}
	if req.Clear && (req.Token != "" || req.Session != "") {
		return contract.RoomDiscordResponse{}, invalid("clear cannot include connection details")
	}
	s.discordChange.Lock()
	defer s.discordChange.Unlock()
	old, err := s.store.DiscordConnection(ctx, room)
	if err != nil {
		return contract.RoomDiscordResponse{}, failure(err)
	}
	c := old
	switch {
	case req.Clear:
		c = store.DiscordConnection{Room: room}
		if err := s.store.SetDiscordConnection(ctx, c); err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
	case req.Token != "":
		client := &discord.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: s.discordEndpoint, Token: strings.TrimSpace(req.Token)}
		bot, guild, err := client.Validate(ctx)
		if err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
		name := req.Session
		if name == "" {
			name = "root"
		}
		session, err := s.store.SessionByName(ctx, room, name)
		if err != nil {
			return contract.RoomDiscordResponse{}, invalid("answering session is not open in this room")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
		c = store.DiscordConnection{Room: room, Guild: guild.ID, GuildName: guild.Name, BotID: bot.ID, BotName: bot.Name, Session: session.ID, Credential: hex.EncodeToString(id[:])}
		if err := s.discordTokens.Write(contract.RoomID(c.Credential), client.Token); err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
		if err := s.store.SetDiscordConnection(ctx, c); err != nil {
			_ = s.discordTokens.Clear(contract.RoomID(c.Credential))
			return contract.RoomDiscordResponse{}, failure(err)
		}
	case req.Session != "":
		return contract.RoomDiscordResponse{}, invalid("session requires a token on stdin")
	}
	if (req.Clear || req.Token != "") && old.Credential != "" {
		_ = s.discordTokens.Clear(contract.RoomID(old.Credential))
	}
	return contract.RoomDiscordResponse{Guild: c.Guild, GuildName: c.GuildName, BotID: c.BotID, BotName: c.BotName, Session: c.Session}, nil
}

// StartDiscord runs only in the engine. Tokens are never mounted into a box.
func (s *Service) StartDiscord(ctx context.Context, tokens *credential.Files, endpoint, gateway string) {
	s.discordTokens = tokens
	s.discordEndpoint = endpoint
	go s.runDiscord(ctx, gateway)
}
