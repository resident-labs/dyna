package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Service) discordClient(ctx context.Context, caller contract.Principal) (*discord.Client, *contract.Error) {
	if caller.Kind != contract.PrincipalSession || s.discordTokens == nil {
		return nil, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return nil, failure(err)
	}
	c, err := s.store.DiscordConnection(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	if c.Guild == "" {
		return nil, invalid("room has no Discord connection")
	}
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return nil, failure(err)
	}
	if caller.Session != c.Session && !tree.Above(caller.Session, c.Session) {
		return nil, forbidden()
	}
	token, err := s.discordTokens.Read(contract.RoomID(c.Credential))
	if err != nil {
		return nil, failure(err)
	}
	return &discord.Client{HTTP: &http.Client{Timeout: 60 * time.Second}, Endpoint: s.discordEndpoint, Token: token}, nil
}
func (s *Service) DiscordAPI(ctx context.Context, caller contract.Principal, req contract.DiscordAPIRequest) (contract.DiscordAPIResponse, *contract.Error) {
	client, problem := s.discordClient(ctx, caller)
	if problem != nil {
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
		var total int64
		for _, attachment := range req.Attachments {
			if !validHash(attachment.SHA256) {
				return contract.DiscordSendResponse{}, invalid("invalid attachment")
			}
			info, problem := s.postFile(ctx, caller, session.Room, attachment.SHA256, "attachment not found")
			if problem != nil {
				return contract.DiscordSendResponse{}, problem
			}
			total += info.Size()
			if total > 25<<20 {
				return contract.DiscordSendResponse{}, invalid("attachments exceed 25 MB")
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
