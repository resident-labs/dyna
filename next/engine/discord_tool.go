package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
	"net/url"
	"os"
	"strings"
)

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
	allowed := false
	for _, r := range routes {
		if caller.Session == r.Session || tree.Above(caller.Session, r.Session) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, forbidden()
	}
	client, err := s.machineDiscordClient(ctx)
	if err != nil {
		return nil, failure(err)
	}
	return client, nil
}

// discordScope checks the URL's resource against all routes, including channel overrides.
func (s *Service) discordScope(ctx context.Context, caller contract.Principal, method, path string) *contract.Error {
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/api/v10/") {
		return forbidden()
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/api/v10/"), "/")
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
		if r.Channel == parent {
			if authorized(*r) {
				return nil
			}
			return forbidden()
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
	client, problem := s.discordClient(ctx, caller)
	if problem != nil {
		return contract.DiscordAPIResponse{}, problem
	}
	if problem := s.discordScope(ctx, caller, req.Method, req.Path); problem != nil {
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
	if req.Channel == "" || strings.IndexFunc(req.Channel, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return contract.DiscordSendResponse{}, invalid("invalid channel ID")
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
