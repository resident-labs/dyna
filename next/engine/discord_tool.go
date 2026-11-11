package engine

import (
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
	"net/url"
	"os"
	"strings"
)

func (s *Service) discordOnPath(session contract.SessionID, routes []store.DiscordRoute, tree room.Tree) bool {
	for _, r := range routes {
		if session == r.Session || tree.Above(session, r.Session) {
			return true
		}
	}
	return false
}
func (s *Service) discordClient(ctx context.Context, caller contract.Principal) (*discord.Client, *contract.Error) {
	if caller.Kind != contract.PrincipalSession || s.discordTokens == nil {
		return nil, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	routes, err := s.store.DiscordRoutes(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	if !s.discordOnPath(caller.Session, routes, tree) {
		return nil, forbidden()
	}
	client, err := s.machineDiscordClient(ctx)
	if err != nil {
		return nil, failure(err)
	}
	return client, nil
}

// discordScope checks the URL's resource against all routes, including channel overrides.
func (s *Service) discordScope(ctx context.Context, caller contract.Principal, method, path string, bodies ...json.RawMessage) *contract.Error {
	if problem := s.discordURLScope(ctx, caller, method, path); problem != nil {
		return problem
	}
	u, _ := url.Parse(path)
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return forbidden()
	}
	check := func(key string, value any) *contract.Error {
		resource := ""
		if key == "guild_id" {
			resource = "guilds"
		} else if strings.HasSuffix(key, "channel_id") {
			resource = "channels"
		} else {
			return nil
		}
		id, ok := value.(string)
		if !ok || !discordID(id) {
			return forbidden()
		}
		return s.discordURLScope(ctx, caller, "GET", "/api/v10/"+resource+"/"+id)
	}
	for key, values := range query {
		for _, value := range values {
			if problem := check(key, value); problem != nil {
				return problem
			}
		}
	}
	var walk func(any) *contract.Error
	walk = func(value any) *contract.Error {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if problem := check(key, child); problem != nil {
					return problem
				}
				if problem := walk(child); problem != nil {
					return problem
				}
			}
		case []any:
			for _, child := range value {
				if problem := walk(child); problem != nil {
					return problem
				}
			}
		}
		return nil
	}
	for _, body := range bodies {
		if len(body) == 0 {
			continue
		}
		var value any
		if json.Unmarshal(body, &value) != nil {
			return invalid("invalid JSON body")
		}
		if problem := walk(value); problem != nil {
			return problem
		}
	}
	return nil
}
func (s *Service) discordURLScope(ctx context.Context, caller contract.Principal, method, path string) *contract.Error {
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/api/v10/") {
		return forbidden()
	}
	if strings.Contains(u.EscapedPath(), "%") || strings.ContainsAny(u.Path, "\\\r\n") {
		return forbidden()
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/api/v10/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return forbidden()
		}
	}
	if len(parts) < 2 || !discordID(parts[1]) {
		return forbidden()
	}
	if parts[0] == "users" && method == "GET" && len(parts) == 2 {
		return nil
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return failure(err)
	}
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		return failure(err)
	}
	authorized := func(r store.DiscordRoute) bool { return r.Room == session.Room }
	if parts[0] == "guilds" {
		for _, r := range routes {
			if r.Guild == parts[1] && r.Channel == "" && authorized(r) {
				return nil
			}
		}
		return forbidden()
	}
	if parts[0] != "channels" {
		return forbidden()
	}
	channel, ok := s.discordDirectory.Channel(parts[1])
	if !ok {
		return forbidden()
	}
	parent := channel.ID
	if channel.Type == 10 || channel.Type == 11 || channel.Type == 12 {
		parent = channel.Parent
	}
	var server *store.DiscordRoute
	for i := range routes {
		r := &routes[i]
		if r.Guild != channel.Guild {
			continue
		}
		if r.Channel == parent && authorized(*r) {
			return nil
		}
		if r.Channel == "" {
			server = r
		}
	}
	if server != nil && authorized(*server) {
		return nil
	}
	return forbidden()
}
func (s *Service) DiscordAPI(ctx context.Context, caller contract.Principal, req contract.DiscordAPIRequest) (contract.DiscordAPIResponse, *contract.Error) {
	if body := strings.TrimSpace(string(req.Body)); strings.HasPrefix(body, `"`) {
		return contract.DiscordAPIResponse{}, invalid("body must be an object or array, not a JSON string")
	}
	client, problem := s.discordClient(ctx, caller)
	if problem != nil {
		return contract.DiscordAPIResponse{}, problem
	}
	if problem := s.discordScope(ctx, caller, req.Method, req.Path, req.Body); problem != nil {
		return contract.DiscordAPIResponse{}, problem
	}
	result, err := client.API(ctx, req.Method, req.Path, req.Body, req.Reason)
	if err != nil {
		return contract.DiscordAPIResponse{}, failure(err)
	}
	return contract.DiscordAPIResponse{Status: result.Status, Body: result.Body}, nil
}
func (s *Service) DiscordSend(ctx context.Context, caller contract.Principal, req contract.DiscordSendRequest) (contract.DiscordSendResponse, *contract.Error) {
	client, problem := s.discordClient(ctx, caller)
	if problem != nil {
		return contract.DiscordSendResponse{}, problem
	}
	if problem := s.discordScope(ctx, caller, "POST", "/api/v10/channels/"+req.Channel+"/messages"); problem != nil {
		return contract.DiscordSendResponse{}, problem
	}
	if len(req.Paths) > 0 {
		return contract.DiscordSendResponse{}, invalid("paths must be uploaded by the box")
	}
	if len(req.Attachments) > 10 {
		return contract.DiscordSendResponse{}, invalid("at most 10 files")
	}
	if len(req.Attachments) > 0 {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.DiscordSendResponse{}, failure(err)
		}
		var files []discord.File
		for _, attachment := range req.Attachments {
			if !validHash(attachment.SHA256) {
				return contract.DiscordSendResponse{}, invalid("invalid attachment")
			}
			info, problem := s.postFile(ctx, caller, session.Room, attachment.SHA256, "attachment not found")
			if problem != nil {
				return contract.DiscordSendResponse{}, problem
			}
			if info.Size() > 10<<20 {
				return contract.DiscordSendResponse{}, invalid("Discord allows at most 10 files and 10 MB each without boosts; this file is larger")
			}
			data, err := os.ReadFile(s.layout.File(session.Room, attachment.SHA256))
			if err != nil {
				return contract.DiscordSendResponse{}, failure(err)
			}
			files = append(files, discord.File{Name: attachment.Name, Data: data})
		}
		chunks := discord.Split(req.Text)
		var ids []string
		text := ""
		if len(chunks) > 0 {
			text = chunks[0]
		}
		id, err := client.SendFiles(ctx, req.Channel, text, req.ReplyTo, files)
		if err != nil {
			return contract.DiscordSendResponse{}, failure(err)
		}
		ids = append(ids, id)
		for _, chunk := range chunks[min(1, len(chunks)):] {
			sent, _, err := client.Send(ctx, req.Channel, chunk, req.ReplyTo)
			if err != nil {
				return contract.DiscordSendResponse{}, failure(err)
			}
			ids = append(ids, sent...)
		}
		return contract.DiscordSendResponse{Messages: ids}, nil
	}
	ids, _, err := client.Send(ctx, req.Channel, req.Text, req.ReplyTo)
	if err != nil {
		return contract.DiscordSendResponse{}, failure(err)
	}
	return contract.DiscordSendResponse{Messages: ids}, nil
}
