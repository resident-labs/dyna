package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/policy"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) DiscordPolicy(ctx context.Context, caller contract.Principal, req contract.DiscordPolicyRequest) (contract.DiscordPolicyResponse, *contract.Error) {
	var roomID contract.RoomID
	root := caller.Kind == contract.PrincipalOwner
	if root {
		roomID = req.Room
		if roomID != "" {
			resolved, _, e := s.store.ResolveRoom(ctx, string(roomID))
			if e != nil {
				return contract.DiscordPolicyResponse{}, invalid("unknown room")
			}
			roomID = resolved
		}
	} else {
		session, e := s.store.Session(ctx, caller.Session)
		if e != nil {
			return contract.DiscordPolicyResponse{}, failure(e)
		}
		roomID = session.Room
		tree, e := s.store.RoomTree(ctx, roomID)
		if e != nil {
			return contract.DiscordPolicyResponse{}, failure(e)
		}
		root = tree.Root() == caller.Session
		if !root {
			if _, p := s.discordClient(ctx, caller); p != nil {
				return contract.DiscordPolicyResponse{}, p
			}
		}
	}
	if req.Text != nil && !root {
		return contract.DiscordPolicyResponse{}, forbidden()
	}
	routes, e := s.store.DiscordRoutes(ctx, roomID)
	if e != nil {
		return contract.DiscordPolicyResponse{}, failure(e)
	}
	var selected *store.DiscordRoute
	for i := range routes {
		r := &routes[i]
		if r.Channel != "" || (req.Server != "" && r.Guild != req.Server) {
			continue
		}
		if selected != nil {
			return contract.DiscordPolicyResponse{}, invalid("server is required when several servers are mapped")
		}
		selected = r
	}
	if selected == nil {
		return contract.DiscordPolicyResponse{}, invalid("no whole-server mapping matches")
	}
	if req.Text == nil {
		return contract.DiscordPolicyResponse{Text: selected.Policy}, nil
	}
	p, e := policy.Decode(*req.Text)
	if e != nil {
		return contract.DiscordPolicyResponse{}, invalid(e.Error())
	}
	server, ok := s.discordDirectory.Server(selected.Guild)
	if !ok {
		return contract.DiscordPolicyResponse{}, invalid("server directory unavailable; wait for Discord to connect")
	}
	resolveChannel := func(name string, category bool) (string, error) {
		matches := []string{}
		for _, c := range s.discordDirectory.Channels() {
			if c.Guild != selected.Guild {
				continue
			}
			if c.Name == name && ((c.Type == 4) == category) {
				matches = append(matches, c.ID)
			}
		}
		if len(matches) != 1 {
			return "", fmt.Errorf("channel %q is missing or ambiguous", name)
		}
		return matches[0], nil
	}
	if p.Log != "" {
		p.Log, e = resolveChannel(p.Log, false)
		if e != nil {
			return contract.DiscordPolicyResponse{}, invalid(e.Error())
		}
	}
	for i, name := range p.Front.Roles {
		matches := []string{}
		for _, r := range server.Roles {
			if r.Name == name {
				matches = append(matches, r.ID)
			}
		}
		if len(matches) != 1 {
			return contract.DiscordPolicyResponse{}, invalid(fmt.Sprintf("role %q is missing or ambiguous", name))
		}
		p.Front.Roles[i] = matches[0]
	}
	for i, name := range p.Front.Quiet {
		category := strings.HasSuffix(name, "/*")
		id, e := resolveChannel(strings.TrimSuffix(name, "/*"), category)
		if e != nil {
			return contract.DiscordPolicyResponse{}, invalid(e.Error())
		}
		if category {
			id += "/*"
		}
		p.Front.Quiet[i] = id
	}
	data, e := json.Marshal(p)
	if e != nil {
		return contract.DiscordPolicyResponse{}, failure(e)
	}
	selected.Policy = *req.Text
	selected.PolicyResolved = string(data)
	if e := s.store.SetDiscordRoute(ctx, *selected, false); e != nil {
		return contract.DiscordPolicyResponse{}, failure(e)
	}
	return contract.DiscordPolicyResponse{Text: *req.Text}, nil
}

func (s *Service) discordCap(ctx context.Context, caller contract.Principal, method, path string, body json.RawMessage) *contract.Error {
	session, e := s.store.Session(ctx, caller.Session)
	if e != nil {
		return failure(e)
	}
	tree, e := s.store.RoomTree(ctx, session.Room)
	if e != nil {
		return failure(e)
	}
	if tree.Root() == caller.Session {
		return nil
	}
	resource, _, _ := strings.Cut(path, "?")
	a := strings.Split(strings.TrimPrefix(resource, "/api/v10/"), "/")
	if len(a) < 2 {
		return forbidden()
	}
	guild := ""
	if a[0] == "guilds" {
		guild = a[1]
	} else if a[0] == "channels" {
		c, ok := s.discordDirectory.Channel(a[1])
		if ok {
			guild = c.Guild
		}
	}
	routes, e := s.store.DiscordRoutes(ctx, session.Room)
	if e != nil {
		return failure(e)
	}
	for _, r := range routes {
		if r.Guild != guild || r.Channel != "" || r.PolicyResolved == "" {
			continue
		}
		var p policy.Policy
		if e := json.Unmarshal([]byte(r.PolicyResolved), &p); e != nil {
			return failure(e)
		}
		if !p.On() {
			return nil
		}
		facts := policy.Facts{Now: time.Now()}
		if method == "PATCH" && len(a) == 4 && a[0] == "channels" && a[2] == "messages" {
			client, e := s.machineDiscordClient(ctx)
			if e != nil {
				return failure(e)
			}
			result, e := client.API(ctx, "GET", resource, nil, "")
			if e != nil {
				return failure(e)
			}
			var message struct {
				Author struct {
					ID string `json:"id"`
				} `json:"author"`
			}
			if result.Status != 200 || json.Unmarshal(result.Body, &message) != nil {
				return invalid("cannot verify message ownership")
			}
			bot, e := s.store.DiscordBot(ctx)
			if e != nil {
				return failure(e)
			}
			facts.OwnMessage = message.Author.ID == bot.ID
		}
		if len(a) == 6 && a[0] == "guilds" && a[2] == "members" && a[4] == "roles" {
			server, ok := s.discordDirectory.Server(guild)
			found := false
			if ok {
				for _, role := range server.Roles {
					if role.ID == a[5] {
						facts.RolePermissions = role.Permissions
						found = true
					}
				}
			}
			if !found {
				return invalid("the policy role no longer resolves; ask a moderator or your parent")
			}
		}
		if e := p.Write(method, path, body, facts); e != nil {
			return invalid(e.Error())
		}
		return nil
	}
	return nil
}
