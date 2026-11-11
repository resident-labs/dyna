package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) machineDiscordClient(ctx context.Context) (*discord.Client, error) {
	if s.discordTokens == nil {
		return nil, errors.New("Discord is unavailable")
	}
	b, err := s.store.DiscordBot(ctx)
	if err != nil {
		return nil, err
	}
	if b.Credential == "" {
		return nil, errors.New("Discord bot is not configured")
	}
	s.discordRuntime.Lock()
	defer s.discordRuntime.Unlock()
	if s.discordHTTP != nil && s.discordClientKey == b.Credential {
		return s.discordHTTP, nil
	}
	token, err := s.discordTokens.Read(contract.RoomID(b.Credential))
	if err != nil {
		return nil, err
	}
	s.discordHTTP = &discord.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: s.discordEndpoint, Token: token}
	s.discordClientKey = b.Credential
	return s.discordHTTP, nil
}
func (s *Service) runDiscord(ctx context.Context, gateway string) {
	var credential string
	var cancel context.CancelFunc
	var done chan struct{}
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		b, err := s.store.DiscordBot(ctx)
		if err == nil && b.Credential != credential {
			if cancel != nil {
				cancel()
				<-done
				cancel = nil
			}
			s.discordDirectory.Reset()
			credential = b.Credential
			if credential != "" {
				client, err := s.machineDiscordClient(ctx)
				if err == nil {
					child, stop := context.WithCancel(ctx)
					cancel = stop
					done = make(chan struct{})
					finished := done
					go func() {
						defer close(finished)
						gatewayDone := make(chan struct{})
						g := discord.Gateway{Client: client, URL: gateway, Bot: b.ID, Directory: &s.discordDirectory, Receive: func(ctx context.Context, m discord.Message) error { return s.discordInput(ctx, client, m) }}
						go func() {
							defer close(gatewayDone)
							if b.Failure != "" {
								return
							}
							if err := g.Run(child); err != nil && child.Err() == nil {
								_ = s.store.FailDiscordGateway(child, b.Credential, err.Error())
							}
						}()
						s.discordDrain(child, client)
						<-gatewayDone
					}()
				} else {
					credential = ""
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) discordInput(ctx context.Context, client *discord.Client, m discord.Message) error {
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		return err
	}
	bot, err := s.store.DiscordBot(ctx)
	if err != nil {
		return err
	}
	var selected *store.DiscordRoute
	channel, known := s.discordDirectory.Channel(m.Channel)
	if m.Guild != "" {
		parent := m.Channel
		if known && (channel.Type == 10 || channel.Type == 11 || channel.Type == 12) {
			parent = channel.Parent
		}
		for i := range routes {
			r := &routes[i]
			if r.Guild != m.Guild {
				continue
			}
			if r.Channel == "" && selected == nil {
				selected = r
			}
			if r.Channel == parent {
				selected = r
				break
			}
		}
	} else {
		guilds := map[string]bool{}
		for i := range routes {
			r := &routes[i]
			if r.Channel != "" || guilds[r.Guild] {
				continue
			}
			member, e := client.API(ctx, "GET", "/api/v10/guilds/"+r.Guild+"/members/"+m.Author.ID, nil, "")
			if e == nil && member.Status == 200 {
				guilds[r.Guild] = true
				_ = json.Unmarshal(member.Body, &m.Member)
				selected = r
			}
		}
		if len(guilds) != 1 {
			_, _, _ = client.Send(ctx, m.Channel, "Please mention me in a mapped server channel so I know which room should answer.", m.ID)
			return nil
		}
	}
	if selected == nil {
		return nil
	}
	guildName := selected.Guild
	var roles []string
	if guild, ok := s.discordDirectory.Server(selected.Guild); ok {
		guildName = guild.Name
		for _, roleID := range m.Member.Roles {
			label := roleID
			for _, role := range guild.Roles {
				if role.ID == roleID {
					label = role.Name
					break
				}
			}
			roles = append(roles, label)
		}
	}
	display := m.Member.Nick
	if display == "" {
		display = m.Author.GlobalName
	}
	if display == "" {
		display = m.Author.Username
	}
	name := m.Channel
	thread := ""
	if known {
		name = channel.Name
		if channel.Type == 10 || channel.Type == 11 || channel.Type == 12 {
			thread = m.Channel
		}
	}
	b := contract.DiscordMessage{Channel: m.Channel, ChannelName: name, Thread: thread, GuildName: guildName, Author: m.Author.Username, DisplayName: display, Roles: roles, AuthorID: m.Author.ID, Owner: bot.Owner != "" && bot.Owner == m.Author.ID, Message: m.ID, Text: m.Content}
	if m.Guild != "" {
		b.History = client.History(ctx, m.Channel, m.ID, bot.ID)
	}
	for _, embed := range m.Embeds {
		if embed.Title != "" {
			b.RichContent += "\n" + embed.Title
		}
		if embed.Description != "" {
			b.RichContent += "\n" + embed.Description
		}
	}
	for _, sticker := range m.Stickers {
		b.RichContent += "\n[sticker] " + sticker.Name
	}
	if m.Poll != nil {
		b.RichContent += "\n[poll] " + m.Poll.Question.Text
	}
	for _, a := range m.Attachments {
		b.Attachments = append(b.Attachments, contract.DiscordAttachment{Name: a.Name, Size: a.Size, URL: a.URL})
	}
	from := contract.Principal{Kind: contract.PrincipalEngine}
	if b.Owner {
		from.Kind = contract.PrincipalOwner
	}
	event, err := s.event(selected.Room, from, b)
	if err != nil {
		return err
	}
	if err := s.store.DiscordInput(ctx, event, selected.Session); err != nil {
		return err
	}
	s.discordRuntime.Lock()
	if s.discordTyping == nil {
		s.discordTyping = map[string]time.Time{}
	}
	s.discordTyping[m.Channel] = time.Now().Add(10 * time.Minute)
	s.discordRuntime.Unlock()
	return nil
}

func (s *Service) discordDrain(ctx context.Context, client *discord.Client) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	typing := map[string]time.Time{}
	for {
		reply, found, err := s.store.NextDiscordReply(ctx)
		if err == nil && found {
			_, status, sendErr := client.SendMessage(ctx, reply.Channel, reply.Text, reply.Message)
			if sendErr == nil || (status >= 400 && status < 500 && status != 429) {
				var notice *contract.Event
				if sendErr != nil {
					event, err := s.event(reply.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionNotice{Session: reply.Session, Text: fmt.Sprintf("Discord reply to channel %s failed permanently (HTTP %d).", reply.Channel, status)})
					if err != nil {
						return
					}
					notice = &event
				}
				if s.store.DiscordReplySent(ctx, reply.ID, notice) == nil {
					s.discordRuntime.Lock()
					delete(s.discordTyping, reply.Channel)
					s.discordRuntime.Unlock()
				}
			} else {
				delay := min(time.Second*time.Duration(1<<min(reply.Attempts, 6)), 60*time.Second)
				var response *discord.SendError
				if errors.As(sendErr, &response) {
					delay = max(delay, response.RetryAfter)
				}
				_ = s.store.RetryDiscordReply(ctx, reply.ID, time.Now().Add(delay))
			}
		}
		var channels []string
		s.discordRuntime.Lock()
		for channel, expiry := range s.discordTyping {
			if time.Now().After(expiry) {
				delete(s.discordTyping, channel)
			} else {
				channels = append(channels, channel)
			}
		}
		s.discordRuntime.Unlock()
		active := map[string]bool{}
		for _, channel := range channels {
			active[channel] = true
			if time.Since(typing[channel]) >= 8*time.Second {
				_, _ = client.API(ctx, "POST", "/api/v10/channels/"+channel+"/typing", nil, "")
				typing[channel] = time.Now()
			}
		}
		for channel := range typing {
			if !active[channel] {
				delete(typing, channel)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
