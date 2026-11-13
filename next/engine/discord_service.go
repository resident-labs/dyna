package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/policy"
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
	s.discordHTTP = &discord.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: s.discordEndpoint, Token: token, BotID: b.ID}
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
						g := discord.Gateway{Client: client, URL: gateway, Bot: b.ID, Directory: &s.discordDirectory, Receive: func(ctx context.Context, m discord.Message) error { return s.discordInput(ctx, client, m) }, Observe: s.discordServerEvent, Lost: &s.discordEventsLost}
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
	if m.Webhook != "" || m.Type == 24 {
		return nil
	}
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
		if len(guilds) != 1 || len(m.Member.Roles) == 0 {
			_, _, _ = client.Send(ctx, m.Channel, "Please mention me in a mapped server channel so I know which room should answer.", m.ID)
			return nil
		}
	}
	if selected == nil {
		return nil
	}
	allowed, err := s.discordPolicyInput(ctx, client, m, routes, *selected)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	if m.Guild == "" {
		if err := s.store.SetDiscordDMRoute(ctx, m.Channel, selected.Guild, selected.Room); err != nil {
			return err
		}
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
	if m.Guild == "" {
		name = "direct message"
	}
	b := contract.DiscordMessage{Direct: m.Guild == "", Channel: m.Channel, ChannelName: name, Thread: thread, GuildName: guildName, Author: m.Author.Username, DisplayName: display, Roles: roles, AuthorID: m.Author.ID, Owner: bot.Owner != "" && bot.Owner == m.Author.ID, Message: m.ID, Text: m.Content}
	b.Confined = b.Direct
	for _, route := range routes {
		if route.Guild != selected.Guild || route.Channel != "" || route.PolicyResolved == "" {
			continue
		}
		var p policy.Policy
		if err := json.Unmarshal([]byte(route.PolicyResolved), &p); err != nil {
			return err
		}
		b.Confined = b.Direct || p.Front.Team != nil && !slices.ContainsFunc(m.Member.Roles, func(r string) bool { return slices.Contains(p.Front.Team, r) })
	}
	if b.Owner {
		b.Confined = false
	}
	if m.Guild != "" {
		b.History = client.History(ctx, m.Channel, m.ID, bot.ID)
	}
	for _, part := range m.Parts() {
		b.RichContent += "\n" + part
	}
	files := m.Files()
	// The message this one answers comes whole, files included: "what do you think" is about that message.
	if r := m.Referenced; r != nil {
		b.RichContent += fmt.Sprintf("\nReply to @%s (message %s): %s", r.Author.Username, r.ID, strings.Join(append([]string{strings.Join(strings.Fields(r.Content), " ")}, r.Parts()...), " | "))
		files = append(files[:len(files):len(files)], r.Files()...)
	}
	for _, a := range files {
		b.Attachments = append(b.Attachments, contract.DiscordAttachment{Name: a.Name, Size: a.Size, URL: a.URL})
	}
	b.Files, b.Attachments = s.downloadDiscordFiles(ctx, selected.Room, b.Attachments)
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
			var status int
			var sendErr error
			if p := s.discordCap(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: reply.Session}, "POST", "/api/v10/channels/"+reply.Channel+"/messages", json.RawMessage(`{"allowed_mentions":{"parse":["users"]}}`)); p != nil {
				status = 403
				sendErr = fmt.Errorf("%s", p.Message)
			} else {
				_, status, sendErr = client.SendMessage(ctx, reply.Channel, reply.Text, reply.Message)
			}
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

// discordServerEvent records a server event in the room that holds the server's whole-server mapping. It is no input: it
// delivers to no session and leaves no reply owed, so only a subscription can wake on it. A server mapped by channel alone has no such room.
func (s *Service) discordServerEvent(ctx context.Context, serverEvent discord.ServerEvent) error {
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.Guild == serverEvent.Guild && route.Channel == "" {
			event, err := s.event(route.Room, contract.Principal{Kind: contract.PrincipalEngine}, serverEvent.Body)
			if err != nil {
				return err
			}
			return s.store.RecordEvent(ctx, event)
		}
	}
	return nil
}
