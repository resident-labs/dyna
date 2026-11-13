package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/credential"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Service) DiscordConfigure(ctx context.Context, caller contract.Principal, req contract.DiscordConfigureRequest) (contract.DiscordConfigureResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.DiscordConfigureResponse{}, forbidden()
	}
	if s.discordTokens == nil {
		return contract.DiscordConfigureResponse{}, invalid("Discord unavailable")
	}
	if req.Clear && (req.Token != "" || req.Owner != nil) {
		return contract.DiscordConfigureResponse{}, invalid("clear cannot include bot details")
	}
	if req.Owner != nil && !discordID(*req.Owner) {
		return contract.DiscordConfigureResponse{}, invalid("invalid Discord owner ID")
	}
	s.discordChange.Lock()
	defer s.discordChange.Unlock()
	old, err := s.store.DiscordBot(ctx)
	if err != nil {
		return contract.DiscordConfigureResponse{}, failure(err)
	}
	b := old
	if req.Clear {
		b = store.DiscordBot{}
	} else {
		if req.Token != "" {
			token := strings.TrimSpace(req.Token)
			if token == "" || len(token) > 16384 || strings.IndexFunc(token, func(r rune) bool {
				return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == '=')
			}) >= 0 {
				return contract.DiscordConfigureResponse{}, invalid("invalid Discord token")
			}
			client := &discord.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: s.discordEndpoint, Token: token}
			result, err := client.API(ctx, "GET", "/api/v10/users/@me", nil, "")
			if err != nil {
				return contract.DiscordConfigureResponse{}, failure(err)
			}
			var bot discord.Identity
			if result.Status != 200 || json.Unmarshal(result.Body, &bot) != nil || bot.ID == "" {
				return contract.DiscordConfigureResponse{}, invalid("Discord rejected bot token")
			}
			var key [16]byte
			if _, err := rand.Read(key[:]); err != nil {
				return contract.DiscordConfigureResponse{}, failure(err)
			}
			b.Failure = ""
			b.ID = bot.ID
			b.Name = bot.Name
			b.Credential = hex.EncodeToString(key[:])
			if err := s.discordTokens.Write(contract.RoomID(b.Credential), token); err != nil {
				return contract.DiscordConfigureResponse{}, failure(err)
			}
		}
		if req.Owner != nil {
			b.Owner = *req.Owner
		}
	}
	if req.Clear || req.Token != "" || req.Owner != nil {
		if err := s.store.SetDiscordBot(ctx, b); err != nil {
			if b.Credential != old.Credential {
				_ = s.discordTokens.Clear(contract.RoomID(b.Credential))
			}
			return contract.DiscordConfigureResponse{}, failure(err)
		}
		if b.Credential != old.Credential && old.Credential != "" {
			_ = s.discordTokens.Clear(contract.RoomID(old.Credential))
		}
	}
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		return contract.DiscordConfigureResponse{}, failure(err)
	}
	out := contract.DiscordConfigureResponse{BotID: b.ID, BotName: b.Name, Owner: b.Owner, GatewayError: b.Failure}
	for _, r := range routes {
		out.Routes = append(out.Routes, contract.DiscordRoute{Guild: r.Guild, Channel: r.Channel, Room: r.Room, Session: r.Session, Instructions: r.Instructions, Policy: r.Policy})
	}
	return out, nil
}
func discordID(id string) bool {
	return id != "" && strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
func (s *Service) RoomDiscord(ctx context.Context, caller contract.Principal, req contract.RoomDiscordRequest) (contract.RoomDiscordResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomDiscordResponse{}, forbidden()
	}
	room, _, err := s.store.ResolveRoom(ctx, string(req.Room))
	if err != nil {
		return contract.RoomDiscordResponse{}, invalid("unknown room")
	}
	if req.Instructions != nil && utf8.RuneCountInString(*req.Instructions) > 4000 {
		return contract.RoomDiscordResponse{}, invalid("instructions exceed 4,000 characters")
	}
	if req.Server != "" {
		if !discordID(req.Server) || (req.Channel != "" && !discordID(req.Channel)) {
			return contract.RoomDiscordResponse{}, invalid("invalid server or channel ID")
		}
		bot, err := s.store.DiscordBot(ctx)
		if err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
		if bot.ID == "" && !req.Remove {
			return contract.RoomDiscordResponse{}, invalid("configure the machine's Discord bot first")
		}
		if !req.Remove {
			client, err := s.machineDiscordClient(ctx)
			if err != nil {
				return contract.RoomDiscordResponse{}, failure(err)
			}
			result, err := client.API(ctx, "GET", "/api/v10/guilds/"+req.Server, nil, "")
			if err != nil || result.Status != 200 {
				return contract.RoomDiscordResponse{}, invalid("bot is not in this server")
			}
			if req.Channel != "" {
				result, err := client.API(ctx, "GET", "/api/v10/channels/"+req.Channel, nil, "")
				var channel discord.Channel
				if err != nil || result.Status != 200 || json.Unmarshal(result.Body, &channel) != nil || channel.Guild != req.Server {
					return contract.RoomDiscordResponse{}, invalid("channel is not in this server")
				}
				s.discordDirectory.Apply("CHANNEL_CREATE", result.Body)
			}
		}
		name := req.Session
		if name == "" {
			name = "root"
		}
		session, err := s.store.SessionByName(ctx, room, name)
		if err != nil {
			return contract.RoomDiscordResponse{}, invalid("answering session is not open in this room")
		}
		instructions := ""
		policyText, policyResolved := "", ""
		previous, err := s.store.DiscordRoutes(ctx, room)
		if err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
		for _, r := range previous {
			if r.Guild == req.Server && r.Channel == req.Channel {
				instructions = r.Instructions
				policyText, policyResolved = r.Policy, r.PolicyResolved
			}
		}
		if req.Instructions != nil {
			instructions = *req.Instructions
		}
		if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: req.Server, Channel: req.Channel, Room: room, Session: session.ID, Instructions: instructions, Policy: policyText, PolicyResolved: policyResolved}, req.Remove); err != nil {
			return contract.RoomDiscordResponse{}, failure(err)
		}
	} else if req.Channel != "" || req.Session != "" || req.Remove || req.Instructions != nil {
		return contract.RoomDiscordResponse{}, invalid("server is required")
	}
	routes, err := s.store.DiscordRoutes(ctx, room)
	if err != nil {
		return contract.RoomDiscordResponse{}, failure(err)
	}
	var out contract.RoomDiscordResponse
	for _, r := range routes {
		out.Routes = append(out.Routes, contract.DiscordRoute{Guild: r.Guild, Channel: r.Channel, Room: r.Room, Session: r.Session, Instructions: r.Instructions, Policy: r.Policy})
	}
	return out, nil
}

// DiscordInstructions lets root shape the session that answers Discord for it: a brief fades at compaction, and these do not.
func (s *Service) DiscordInstructions(ctx context.Context, caller contract.Principal, req contract.DiscordInstructionsRequest) (contract.DiscordInstructionsResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.DiscordInstructionsResponse{}, forbidden()
	}
	root, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.DiscordInstructionsResponse{}, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, root.Room)
	if err != nil {
		return contract.DiscordInstructionsResponse{}, failure(err)
	}
	if tree.Root() != caller.Session {
		return contract.DiscordInstructionsResponse{}, forbidden()
	}
	if req.Text != nil && utf8.RuneCountInString(*req.Text) > 4000 {
		return contract.DiscordInstructionsResponse{}, invalid("instructions exceed 4,000 characters")
	}
	answering, err := s.store.SessionByName(ctx, root.Room, req.Session)
	if err != nil {
		return contract.DiscordInstructionsResponse{}, invalid("session is not open in this room")
	}
	routes, err := s.store.DiscordRoutes(ctx, root.Room)
	if err != nil {
		return contract.DiscordInstructionsResponse{}, failure(err)
	}
	routes = slices.DeleteFunc(routes, func(r store.DiscordRoute) bool { return r.Session != answering.ID })
	if len(routes) == 0 {
		return contract.DiscordInstructionsResponse{}, invalid("that session answers no Discord mapping of this room")
	}
	if req.Text == nil {
		return contract.DiscordInstructionsResponse{Text: routes[0].Instructions}, nil
	}
	for _, r := range routes {
		r.Instructions = *req.Text
		if err := s.store.SetDiscordRoute(ctx, r, false); err != nil {
			return contract.DiscordInstructionsResponse{}, failure(err)
		}
	}
	return contract.DiscordInstructionsResponse{Text: *req.Text}, nil
}
func (s *Service) StartDiscord(ctx context.Context, tokens *credential.Files, endpoint, gateway string) {
	s.discordTokens = tokens
	s.discordEndpoint = endpoint
	go s.runDiscord(ctx, gateway)
}
