package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"time"
)

// DiscordBot is machine metadata. Credential is an opaque file reference, never a token.
type DiscordBot struct{ ID, Name, Owner, Credential, Failure string }

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
	Policy         string             `json:"policy,omitempty"`
	PolicyResolved string             `json:"policy_resolved,omitempty"`
	Instructions   string             `json:"instructions,omitempty"`
	Guild          string             `json:"guild"`
	Channel        string             `json:"channel,omitempty"`
	Room           contract.RoomID    `json:"room"`
	Session        contract.SessionID `json:"session"`
}

func (s *Store) DiscordRoutes(ctx context.Context, room contract.RoomID) ([]DiscordRoute, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT guild,channel,room,instructions,policy,policy_resolved,CASE WHEN EXISTS(SELECT 1 FROM sessions WHERE id=d.session AND closed_at IS NULL) THEN session ELSE (SELECT id FROM sessions WHERE room=d.room AND parent IS NULL AND closed_at IS NULL) END FROM discord_routes d WHERE ?='' OR room=? ORDER BY guild,channel`, room, room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []DiscordRoute
	for rows.Next() {
		var r DiscordRoute
		if err := rows.Scan(&r.Guild, &r.Channel, &r.Room, &r.Instructions, &r.Policy, &r.PolicyResolved, &r.Session); err != nil {
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
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_routes(guild,channel,room,session,instructions,policy,policy_resolved) VALUES(?,?,?,?,?,?,?) ON CONFLICT(guild,channel) DO UPDATE SET room=excluded.room,session=excluded.session,instructions=excluded.instructions,policy=excluded.policy,policy_resolved=excluded.policy_resolved`, r.Guild, r.Channel, r.Room, r.Session, r.Instructions, r.Policy, r.PolicyResolved)
		return err
	})
}

func (s *Store) DiscordInput(ctx context.Context, event contract.Event, to contract.SessionID) error {
	b := event.Body.(contract.DiscordMessage)
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT INTO discord_messages(message,room) VALUES(?,?) ON CONFLICT(message) DO NOTHING", b.Message, event.Room)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil || inserted == 0 {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceDiscord, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalEngine}, Home: contract.Conversation{Kind: contract.ConversationDiscord, Channel: b.Channel}, ReplyTo: event.ID}})
	})
}

type DiscordOutbox struct {
	Attempts                      int
	ID                            int64
	Room                          contract.RoomID
	Session                       contract.SessionID
	Channel, Message, Text, Group string
}

func (s *Store) NextDiscordReply(ctx context.Context) (DiscordOutbox, bool, error) {
	var o DiscordOutbox
	err := s.Read().QueryRowContext(ctx, `SELECT id,attempts,room,session,channel,message,text,group_key FROM discord_outbox o WHERE due_at<=? AND NOT EXISTS(SELECT 1 FROM discord_outbox earlier WHERE earlier.group_key=o.group_key AND earlier.id<o.id) ORDER BY id LIMIT 1`, time.Now().UnixMilli()).Scan(&o.ID, &o.Attempts, &o.Room, &o.Session, &o.Channel, &o.Message, &o.Text, &o.Group)
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
		query := "DELETE FROM discord_outbox WHERE id=?"
		if notice != nil {
			query = "DELETE FROM discord_outbox WHERE group_key=(SELECT group_key FROM discord_outbox WHERE id=?)"
		}
		_, err := tx.ExecContext(ctx, query, id)
		return err
	})
}
func (s *Store) RetryDiscordReply(ctx context.Context, id int64, due time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE discord_outbox SET attempts=attempts+1,due_at=? WHERE id=?", due.UnixMilli(), id)
		return err
	})
}

func (s *Store) FailDiscordGateway(ctx context.Context, credential, reason string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='discord_bot'").Scan(&raw); err != nil {
			return err
		}
		var b DiscordBot
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			return err
		}
		if b.Credential != credential {
			return nil
		}
		b.Failure = reason
		data, err := json.Marshal(b)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key='discord_bot'", string(data))
		return err
	})
}

// DiscordOwned is an object `discord apply` created or adopted in a room's server.
// An empty ID means a create was started and its outcome is not recorded yet.
type DiscordOwned struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	ID      string `json:"id"`
	Channel string `json:"channel,omitempty"` // for a message, the channel it is in
	Content string `json:"content,omitempty"` // for a message, what was last posted
}

func (s *Store) DiscordOwned(ctx context.Context, room contract.RoomID, guild string) ([]DiscordOwned, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT kind,path,id,channel,content FROM discord_owned WHERE room=? AND guild=? ORDER BY kind,path`, room, guild)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owned []DiscordOwned
	for rows.Next() {
		var o DiscordOwned
		if err := rows.Scan(&o.Kind, &o.Path, &o.ID, &o.Channel, &o.Content); err != nil {
			return nil, err
		}
		owned = append(owned, o)
	}
	return owned, rows.Err()
}

// SetDiscordOwned records one object. A recorded Discord ID has one row, so a
// renamed object replaces the row it had under its old path.
func (s *Store) SetDiscordOwned(ctx context.Context, room contract.RoomID, guild string, o DiscordOwned) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if o.ID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM discord_owned WHERE room=? AND guild=? AND kind=? AND id=?`, room, guild, o.Kind, o.ID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_owned(room,guild,kind,path,id,channel,content) VALUES(?,?,?,?,?,?,?) ON CONFLICT(room,guild,kind,path) DO UPDATE SET id=excluded.id,channel=excluded.channel,content=excluded.content`, room, guild, o.Kind, o.Path, o.ID, o.Channel, o.Content)
		return err
	})
}

func (s *Store) DeleteDiscordOwned(ctx context.Context, room contract.RoomID, guild, kind, path string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM discord_owned WHERE room=? AND guild=? AND kind=? AND path=?`, room, guild, kind, path)
		return err
	})
}
