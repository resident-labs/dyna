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
		if req.Room == "" {
			return contract.DiscordPolicyResponse{}, invalid("room is required for owner callers")
		}
		roomID = req.Room
		if roomID != "" {
			resolved, _, err := s.store.ResolveRoom(ctx, string(roomID))
			if err != nil {
				return contract.DiscordPolicyResponse{}, invalid("unknown room")
			}
			roomID = resolved
		}
	} else {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.DiscordPolicyResponse{}, failure(err)
		}
		roomID = session.Room
		tree, err := s.store.RoomTree(ctx, roomID)
		if err != nil {
			return contract.DiscordPolicyResponse{}, failure(err)
		}
		root = tree.Root() == caller.Session
		if !root {
			if _, resolvedPolicy := s.discordClient(ctx, caller); resolvedPolicy != nil {
				return contract.DiscordPolicyResponse{}, resolvedPolicy
			}
		}
	}
	if req.Text != nil && !root {
		return contract.DiscordPolicyResponse{}, forbidden()
	}
	routes, err := s.store.DiscordRoutes(ctx, roomID)
	if err != nil {
		return contract.DiscordPolicyResponse{}, failure(err)
	}
	var selected *store.DiscordRoute
	for i := range routes {
		mapping := &routes[i]
		if mapping.Channel != "" || (req.Server != "" && mapping.Guild != req.Server) {
			continue
		}
		if selected != nil {
			return contract.DiscordPolicyResponse{}, invalid("server is required when several servers are mapped")
		}
		selected = mapping
	}
	if selected == nil {
		return contract.DiscordPolicyResponse{}, invalid("no whole-server mapping matches")
	}
	if req.Text == nil {
		return contract.DiscordPolicyResponse{Text: selected.Policy}, nil
	}
	return s.setDiscordPolicy(ctx, *selected, *req.Text)
}

// setDiscordPolicy is the text-and-route entry point shared with the future discord apply.
func (s *Service) setDiscordPolicy(ctx context.Context, route store.DiscordRoute, text string) (contract.DiscordPolicyResponse, *contract.Error) {
	selected := &route
	resolvedPolicy, err := policy.Decode(text)
	if err != nil {
		return contract.DiscordPolicyResponse{}, invalid(err.Error())
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
	if resolvedPolicy.Log != "" {
		resolvedPolicy.Log, err = resolveChannel(resolvedPolicy.Log, false)
		if err != nil {
			return contract.DiscordPolicyResponse{}, invalid(err.Error())
		}
	}
	for i, name := range resolvedPolicy.Front.Roles {
		matches := []string{}
		for _, role := range server.Roles {
			if role.Name == name {
				matches = append(matches, role.ID)
			}
		}
		if len(matches) != 1 {
			return contract.DiscordPolicyResponse{}, invalid(fmt.Sprintf("role %q is missing or ambiguous", name))
		}
		resolvedPolicy.Front.Roles[i] = matches[0]
	}
	for i, name := range resolvedPolicy.Front.Quiet {
		category := strings.HasSuffix(name, "/*")
		id, err := resolveChannel(strings.TrimSuffix(name, "/*"), category)
		if err != nil {
			return contract.DiscordPolicyResponse{}, invalid(err.Error())
		}
		if category {
			id += "/*"
		}
		resolvedPolicy.Front.Quiet[i] = id
	}
	data, err := json.Marshal(resolvedPolicy)
	if err != nil {
		return contract.DiscordPolicyResponse{}, failure(err)
	}
	selected.Policy = text
	selected.PolicyResolved = string(data)
	if err := s.store.SetDiscordRoute(ctx, *selected, false); err != nil {
		return contract.DiscordPolicyResponse{}, failure(err)
	}
	return contract.DiscordPolicyResponse{Text: text}, nil
}

func (s *Service) discordCap(ctx context.Context, caller contract.Principal, method, path string, body json.RawMessage) *contract.Error {
	_, problem := s.discordPrepare(ctx, caller, method, path, body)
	return problem
}
func (s *Service) discordPrepare(ctx context.Context, caller contract.Principal, method, path string, body json.RawMessage) (json.RawMessage, *contract.Error) {
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	if tree.Root() == caller.Session {
		return body, nil
	}
	route := policy.ParseRoute(method, path)
	guild := ""
	switch route.Resource {
	case policy.Guild:
		guild = route.ResourceID
	case policy.Channel:
		channel, known := s.discordDirectory.Channel(route.ResourceID)
		if !known {
			return nil, forbidden()
		}
		guild = channel.Guild
	}
	routes, err := s.store.DiscordRoutes(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	for _, mapping := range routes {
		if mapping.Guild != guild || mapping.Channel != "" || mapping.PolicyResolved == "" {
			continue
		}
		var resolvedPolicy policy.Policy
		if err := json.Unmarshal([]byte(mapping.PolicyResolved), &resolvedPolicy); err != nil {
			return nil, failure(err)
		}
		if !resolvedPolicy.On() {
			return body, nil
		}
		facts := policy.Facts{Now: time.Now()}
		if route.Action == policy.MessageEdit || route.Action == policy.PollExpire {
			client, err := s.machineDiscordClient(ctx)
			if err != nil {
				return nil, failure(err)
			}
			result, err := client.API(ctx, "GET", "/api/v10/channels/"+route.ResourceID+"/messages/"+route.MessageID, nil, "")
			if err != nil {
				return nil, failure(err)
			}
			var message struct {
				Author struct {
					ID string `json:"id"`
				} `json:"author"`
			}
			if result.Status != 200 || json.Unmarshal(result.Body, &message) != nil {
				return nil, &contract.Error{Code: contract.ErrForbidden, Message: "cannot verify message ownership"}
			}
			bot, err := s.store.DiscordBot(ctx)
			if err != nil {
				return nil, failure(err)
			}
			facts.OwnMessage = message.Author.ID == bot.ID
		}
		if route.Action == policy.MemberRole {
			server, ok := s.discordDirectory.Server(guild)
			found := false
			if ok {
				for _, role := range server.Roles {
					if role.ID == route.RoleID {
						facts.RolePermissions = role.Permissions
						found = true
					}
				}
			}
			if !found {
				return nil, &contract.Error{Code: contract.ErrForbidden, Message: "the policy role no longer resolves; ask a moderator or your parent"}
			}
		}
		normalized, err := resolvedPolicy.Authorize(route, body, facts)
		if err != nil {
			return nil, &contract.Error{Code: contract.ErrForbidden, Message: err.Error()}
		}
		return normalized, nil
	}
	return body, nil
}
