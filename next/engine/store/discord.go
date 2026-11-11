package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
)

// DiscordBot is machine metadata. Credential is an opaque file reference, never a token.
type DiscordBot struct{ ID, Name, Owner, Credential string }

func (s *Store) DiscordBot(ctx context.Context) (DiscordBot, error) {
	var b DiscordBot
	var data string
	err := s.Read().QueryRowContext(ctx, `SELECT value FROM meta WHERE key='discord_bot'`).Scan(&data)
	if err == sql.ErrNoRows {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	err = json.Unmarshal([]byte(data), &b)
	return b, err
}
func (s *Store) SetDiscordBot(ctx context.Context, b DiscordBot) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('discord_bot',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(data))
		return err
	})
}

type DiscordRoute struct {
	Instructions string             `json:"instructions,omitempty"`
	Guild        string             `json:"guild"`
	Channel      string             `json:"channel,omitempty"`
	Room         contract.RoomID    `json:"room"`
	Session      contract.SessionID `json:"session"`
}

func (s *Store) DiscordRoutes(ctx context.Context, room contract.RoomID) ([]DiscordRoute, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT guild,channel,room,instructions,CASE WHEN EXISTS(SELECT 1 FROM sessions WHERE id=d.session AND closed_at IS NULL) THEN session ELSE (SELECT id FROM sessions WHERE room=d.room AND parent IS NULL AND closed_at IS NULL) END FROM discord_routes d WHERE ?='' OR room=? ORDER BY guild,channel`, room, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []DiscordRoute
	for rows.Next() {
		var r DiscordRoute
		if err := rows.Scan(&r.Guild, &r.Channel, &r.Room, &r.Instructions, &r.Session); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	return routes, rows.Err()
}
func (s *Store) SetDiscordRoute(ctx context.Context, r DiscordRoute, remove bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if remove {
			_, err := tx.ExecContext(ctx, `DELETE FROM discord_routes WHERE guild=? AND channel=? AND room=?`, r.Guild, r.Channel, r.Room)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_routes(guild,channel,room,session,instructions) VALUES(?,?,?,?,?) ON CONFLICT(guild,channel) DO UPDATE SET room=excluded.room,session=excluded.session,instructions=excluded.instructions`, r.Guild, r.Channel, r.Room, r.Session, r.Instructions)
		return err
	})
}

func (s *Store) DiscordInput(ctx context.Context, event contract.Event, to contract.SessionID) error {
	b := event.Body.(contract.DiscordMessage)
	return s.Write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE room=? AND type='discord.message' AND json_extract(body,'$.message')=?)`, event.Room, b.Message).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: "discord", Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalEngine}, Home: contract.Conversation{Kind: "discord", Channel: b.Channel}, ReplyTo: event.ID}})
	})
}

type DiscordOutbox struct {
	ID                     int64
	Room                   contract.RoomID
	Session                contract.SessionID
	Channel, Message, Text string
}

func (s *Store) NextDiscordReply(ctx context.Context) (DiscordOutbox, bool, error) {
	var o DiscordOutbox
	err := s.Read().QueryRowContext(ctx, `SELECT id,room,session,channel,message,text FROM discord_outbox ORDER BY id LIMIT 1`).Scan(&o.ID, &o.Room, &o.Session, &o.Channel, &o.Message, &o.Text)
	if err == sql.ErrNoRows {
		return o, false, nil
	}
	return o, err == nil, err
}
func (s *Store) DiscordReplySent(ctx context.Context, id int64, notice *contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if notice != nil {
			seq, err := s.AppendEvent(tx, *notice)
			if err != nil {
				return err
			}
			b := notice.Body.(contract.SessionNotice)
			if err := s.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: b.Session}, Surface: contract.SurfaceEngine, Wake: true}); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM discord_outbox WHERE id=?", id)
		return err
	})
}
func (s *Store) DiscordTyping(ctx context.Context) ([]string, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT DISTINCT substr(d.reply_home,9) FROM deliveries d JOIN turns t ON t.id=d.turn WHERE t.ended_at IS NULL AND d.reply_home LIKE 'discord:%' UNION SELECT json_extract(j.value,'$.home.channel') FROM replies_owed o JOIN turns t ON t.session=o.session CROSS JOIN json_each(o.replies) j WHERE t.ended_at IS NULL AND json_extract(j.value,'$.home.kind')='discord'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []string
	for rows.Next() {
		var channel string
		if err := rows.Scan(&channel); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}
