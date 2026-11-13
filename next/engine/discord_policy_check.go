package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/policy"
)

func (s *Service) DiscordCheck(ctx context.Context, caller contract.Principal, req contract.DiscordCheckRequest) (contract.DiscordCheckResponse, *contract.Error) {
	roomID := req.Room
	if caller.Kind != contract.PrincipalOwner {
		session, e := s.store.Session(ctx, caller.Session)
		if e != nil {
			return contract.DiscordCheckResponse{}, failure(e)
		}
		roomID = session.Room
		tree, e := s.store.RoomTree(ctx, roomID)
		if e != nil {
			return contract.DiscordCheckResponse{}, failure(e)
		}
		if tree.Root() != caller.Session {
			return contract.DiscordCheckResponse{}, forbidden()
		}
	}
	read, problem := s.DiscordPolicy(ctx, caller, contract.DiscordPolicyRequest{Room: roomID, Server: req.Server})
	if problem != nil {
		return contract.DiscordCheckResponse{}, problem
	}
	routes, e := s.store.DiscordRoutes(ctx, roomID)
	if e != nil {
		return contract.DiscordCheckResponse{}, failure(e)
	}
	guildID := req.Server
	for _, r := range routes {
		if r.Channel == "" && (guildID == "" || guildID == r.Guild) {
			guildID = r.Guild
		}
	}
	server, ok := s.discordDirectory.Server(guildID)
	if !ok {
		return contract.DiscordCheckResponse{}, invalid("server directory unavailable")
	}
	out := contract.DiscordCheckResponse{Lines: []string{"Webhook messages and AutoMod alert messages never start a turn.", "Enable the privileged Server Members and Message Content intents in the Discord Developer Portal; the gateway requests both."}}
	if read.Text == "" {
		out.Lines = append(out.Lines, "No front policy is set.")
		return out, nil
	}
	p, e := policy.Decode(read.Text)
	if e != nil {
		return out, failure(e)
	}
	bot, e := s.store.DiscordBot(ctx)
	if e != nil {
		return out, failure(e)
	}
	client, e := s.machineDiscordClient(ctx)
	if e != nil {
		return out, failure(e)
	}
	result, e := client.API(ctx, "GET", "/api/v10/guilds/"+guildID+"/members/"+bot.ID, nil, "")
	if e != nil {
		return out, failure(e)
	}
	var member struct {
		Roles []string `json:"roles"`
	}
	if result.Status != 200 || json.Unmarshal(result.Body, &member) != nil {
		return out, invalid("cannot read the bot's server member roles")
	}
	mine := map[string]bool{guildID: true}
	for _, id := range member.Roles {
		mine[id] = true
	}
	var permissions uint64
	highest := -1
	for _, r := range server.Roles {
		if mine[r.ID] {
			permissions |= r.Permissions
			if r.Position > highest {
				highest = r.Position
			}
		}
	}
	for _, name := range p.Front.Roles {
		var found []discord.Role
		for _, r := range server.Roles {
			if r.Name == name {
				found = append(found, r)
			}
		}
		if len(found) != 1 {
			out.Lines = append(out.Lines, fmt.Sprintf("The policy role %q no longer resolves uniquely; set the policy again.", name))
			continue
		}
		role := found[0]
		if highest <= role.Position {
			out.Lines = append(out.Lines, fmt.Sprintf("Drag the dyna role above %s in Server Settings > Roles.", name))
		} else {
			out.Lines = append(out.Lines, fmt.Sprintf("The dyna role is above %s.", name))
		}
		if role.Managed {
			out.Lines = append(out.Lines, fmt.Sprintf("The role %s is managed by an integration and cannot be assigned.", name))
		}
		if role.Permissions&policy.DangerousPermissions != 0 {
			out.Lines = append(out.Lines, fmt.Sprintf("The role %s carries dangerous permissions and the answering session cannot manage it.", name))
		}
	}
	names := append([]string{}, p.Front.Quiet...)
	if p.Log != "" {
		names = append(names, p.Log)
	}
	for _, name := range names {
		count := 0
		category := strings.HasSuffix(name, "/*")
		for _, c := range server.Channels {
			if c.Name == strings.TrimSuffix(name, "/*") && ((c.Type == 4) == category) {
				count++
			}
		}
		if count != 1 {
			out.Lines = append(out.Lines, fmt.Sprintf("The policy channel %q no longer resolves uniquely; set the policy again.", name))
		}
	}
	required := []struct {
		name string
		bit  uint64
	}{{"View Channels", 1 << 10}, {"Send Messages", 1 << 11}, {"Read Message History", 1 << 16}, {"Add Reactions", 1 << 6}, {"Pin Messages", 1 << 51}}
	if p.Front.Delete {
		required = append(required, struct {
			name string
			bit  uint64
		}{"Manage Messages", 1 << 13})
	}
	if p.Front.TimeoutMax != "" {
		required = append(required, struct {
			name string
			bit  uint64
		}{"Moderate Members", 1 << 40})
	}
	if len(p.Front.Roles) > 0 {
		required = append(required, struct {
			name string
			bit  uint64
		}{"Manage Roles", 1 << 28})
	}
	if p.Front.Threads {
		required = append(required, struct {
			name string
			bit  uint64
		}{"Create Public Threads", 1 << 35}, struct {
			name string
			bit  uint64
		}{"Create Private Threads", 1 << 36})
	}
	for _, need := range required {
		if permissions&(1<<3) == 0 && permissions&need.bit == 0 {
			out.Lines = append(out.Lines, "Grant the bot "+need.name+" in Server Settings > Roles.")
		}
	}
	return out, nil
}
