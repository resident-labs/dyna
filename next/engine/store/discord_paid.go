package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/resident-labs/dyna/contract"
)

const payDiscordDelivery = `UPDATE deliveries SET reply_kind='none'
 WHERE to_kind='session' AND to_id=? AND reply_home=? AND reply_kind='final'
 AND turn IN (SELECT id FROM turns WHERE session=? AND ended_at IS NULL)
 AND EXISTS (SELECT 1 FROM events WHERE events.id=deliveries.reply_to
 AND events.type='discord.message' AND json_extract(events.body,'$.message')=?)`

// PayDiscordReply settles only the input explicitly answered by a successful send.
// The existing delivery reply kind and replies_owed survive engine restarts.
func (s *Store) PayDiscordReply(ctx context.Context, session contract.SessionID, channel, message string) (bool, error) {
	paid := false
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, payDiscordDelivery, session, "discord:"+channel, session, message)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		paid = n > 0
		var data string
		err = tx.QueryRowContext(ctx, "SELECT replies FROM replies_owed WHERE session=?", session).Scan(&data)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var replies []contract.ReplyChannel
		if err := json.Unmarshal([]byte(data), &replies); err != nil {
			return err
		}
		kept := replies[:0]
		for _, reply := range replies {
			match := false
			if reply.Home.Kind == contract.ConversationDiscord && reply.Home.Channel == channel {
				if err := tx.QueryRowContext(ctx, "SELECT json_extract(body,'$.message')=? FROM events WHERE id=?", message, reply.ReplyTo).Scan(&match); err != nil {
					return err
				}
			}
			if match {
				paid = true
			} else {
				kept = append(kept, reply)
			}
		}
		if len(kept) == 0 {
			_, err = tx.ExecContext(ctx, "DELETE FROM replies_owed WHERE session=?", session)
			return err
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE replies_owed SET replies=? WHERE session=?", string(encoded), session)
		return err
	})
	return paid, err
}
