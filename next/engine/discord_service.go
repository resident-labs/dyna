package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) runDiscord(ctx context.Context, gateway string) {
	type running struct {
		credential string
		cancel     context.CancelFunc
		done       chan struct{}
	}
	active := map[contract.RoomID]running{}
	defer func() {
		for _, r := range active {
			r.cancel()
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		rooms, err := s.store.DiscordRooms(ctx)
		if err == nil {
			seen := map[contract.RoomID]bool{}
			for _, room := range rooms {
				seen[room] = true
				c, err := s.store.DiscordConnection(ctx, room)
				if err != nil {
					continue
				}
				r, ok := active[room]
				if ok && r.credential == c.Credential {
					continue
				}
				if ok {
					r.cancel()
					<-r.done
					delete(active, room)
				}
				token, err := s.discordTokens.Read(contract.RoomID(c.Credential))
				if err != nil {
					continue
				}
				client := &discord.Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Endpoint: s.discordEndpoint, Token: token}
				child, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				active[room] = running{c.Credential, cancel, done}
				go func() {
					defer close(done)
					g := discord.Gateway{Client: client, URL: gateway, Guild: c.Guild, Bot: c.BotID, Receive: func(ctx context.Context, m discord.Message) error { return s.discordInput(ctx, c, client, m) }}
					go g.Run(child)
					s.discordDrain(child, c, client)
				}()
			}
			for room, r := range active {
				if !seen[room] {
					r.cancel()
					<-r.done
					delete(active, room)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (s *Service) discordInput(ctx context.Context, c store.DiscordConnection, client *discord.Client, m discord.Message) error {
	current, err := s.store.DiscordConnection(ctx, c.Room)
	if err != nil {
		return err
	}
	if current.Credential != c.Credential {
		return nil
	}
	name := m.Channel
	thread := ""
	r, err := client.API(ctx, "GET", "/api/v10/channels/"+m.Channel, nil, "")
	if err == nil && r.Status == 200 {
		var channel struct {
			Name string `json:"name"`
			Type int    `json:"type"`
		}
		if json.Unmarshal(r.Body, &channel) == nil {
			if channel.Name != "" {
				name = channel.Name
			}
			if channel.Type == 10 || channel.Type == 11 || channel.Type == 12 {
				thread = m.Channel
			}
		}
	}
	b := contract.DiscordMessage{Channel: m.Channel, ChannelName: name, Thread: thread, GuildName: c.GuildName, Author: m.Author.Username, AuthorID: m.Author.ID, Message: m.ID, Text: m.Content}
	if m.Guild != "" {
		b.History = client.History(ctx, m.Channel, m.ID, c.BotID)
	}
	for _, a := range m.Attachments {
		b.Attachments = append(b.Attachments, a.URL)
	}
	event, err := s.event(c.Room, contract.Principal{Kind: contract.PrincipalEngine}, b)
	if err != nil {
		return err
	}
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE room=? AND type='discord.message' AND json_extract(body,'$.message')=?)`, c.Room, m.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.store.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: current.Session}, Surface: "discord", Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalEngine}, Home: contract.Conversation{Kind: "discord", Channel: m.Channel}, ReplyTo: event.ID}})
	})
}
func (s *Service) discordDrain(ctx context.Context, c store.DiscordConnection, client *discord.Client) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastTyping := time.Time{}
	for {
		var id int64
		var session contract.SessionID
		var channel, message, text string
		err := s.store.Read().QueryRowContext(ctx, `SELECT id,session,channel,message,text FROM discord_outbox WHERE room=? ORDER BY id LIMIT 1`, c.Room).Scan(&id, &session, &channel, &message, &text)
		if err == nil {
			_, status, sendErr := client.Send(ctx, channel, text, message)
			if sendErr == nil || status == 403 || status == 404 {
				_ = s.store.Write(ctx, func(tx *sql.Tx) error {
					if sendErr != nil {
						b := contract.SessionNotice{Session: session, Text: fmt.Sprintf("Discord reply to channel %s failed permanently (HTTP %d).", channel, status)}
						event, e := s.event(c.Room, contract.Principal{Kind: contract.PrincipalEngine}, b)
						if e != nil {
							return e
						}
						seq, e := s.store.AppendEvent(tx, event)
						if e != nil {
							return e
						}
						if e = s.store.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: session}, Surface: contract.SurfaceEngine, Wake: true}); e != nil {
							return e
						}
					}
					_, e := tx.ExecContext(ctx, `DELETE FROM discord_outbox WHERE id=?`, id)
					return e
				})
			}
		}
		if time.Since(lastTyping) >= 8*time.Second {
			lastTyping = time.Now()
			rows, e := s.store.Read().QueryContext(ctx, `SELECT DISTINCT substr(d.reply_home,9) FROM deliveries d JOIN turns t ON t.id=d.turn WHERE t.ended_at IS NULL AND d.reply_home LIKE 'discord:%' AND t.session IN (SELECT id FROM sessions WHERE room=?)`, c.Room)
			if e == nil {
				var channels []string
				for rows.Next() {
					var channel string
					if rows.Scan(&channel) == nil {
						channels = append(channels, channel)
					}
				}
				rows.Close()
				for _, channel := range channels {
					_, _ = client.API(ctx, "POST", "/api/v10/channels/"+channel+"/typing", nil, "")
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
