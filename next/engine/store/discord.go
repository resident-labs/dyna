package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
)

type DiscordConnection struct {
	Room                             contract.RoomID
	Guild, GuildName, BotID, BotName string
	Session                          contract.SessionID
	Credential                       string
}

func (s *Store) DiscordConnection(ctx context.Context, room contract.RoomID) (DiscordConnection, error) {
	var c DiscordConnection
	err := s.Read().QueryRowContext(ctx, `SELECT room,guild,guild_name,bot_id,bot_name,CASE WHEN EXISTS(SELECT 1 FROM sessions WHERE id=d.session AND closed_at IS NULL) THEN session ELSE (SELECT id FROM sessions WHERE room=d.room AND parent IS NULL AND closed_at IS NULL) END,credential FROM room_discord d WHERE room=?`, room).Scan(&c.Room, &c.Guild, &c.GuildName, &c.BotID, &c.BotName, &c.Session, &c.Credential)
	if err == sql.ErrNoRows {
		return DiscordConnection{}, nil
	}
	return c, err
}
func (s *Store) SetDiscordConnection(ctx context.Context, c DiscordConnection) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if c.Guild == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM room_discord WHERE room=?`, c.Room)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO room_discord(room,guild,guild_name,bot_id,bot_name,session,credential) VALUES(?,?,?,?,?,?,?) ON CONFLICT(room) DO UPDATE SET guild=excluded.guild,guild_name=excluded.guild_name,bot_id=excluded.bot_id,bot_name=excluded.bot_name,session=excluded.session,credential=excluded.credential`, c.Room, c.Guild, c.GuildName, c.BotID, c.BotName, c.Session, c.Credential)
		return err
	})
}
func (s *Store) DiscordRooms(ctx context.Context) ([]contract.RoomID, error) {
	rows, err := s.Read().QueryContext(ctx, `SELECT room FROM room_discord ORDER BY room`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rooms []contract.RoomID
	for rows.Next() {
		var room contract.RoomID
		if err := rows.Scan(&room); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	return rooms, rows.Err()
}
