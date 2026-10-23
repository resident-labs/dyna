package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/database"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// TODO: legacy/shim remove later — persist boundaries for retained terminal turns.
func (s *Store) migrateTurnCutoffs() error {
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	if err := sessionlife.Prepare(s.root()); err != nil {
		return err
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key='subagent.turns.cutoff'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version == "1" {
		return nil
	}
	if err := database.BackUpOnce(db); err != nil {
		return fmt.Errorf("back up dyna.db before converting cutoffs: %w", err)
	}
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT turns.session,turns.n,turns.turn,COALESCE(records.room_id,'')
 FROM session_turns AS turns LEFT JOIN session_records AS records ON records.id=turns.session
 WHERE json_extract(CAST(turns.turn AS TEXT),'$.inputCutoff') IS NULL
 AND json_extract(CAST(turns.turn AS TEXT),'$.status') IN ('ok','error','stopped')
 ORDER BY turns.session,turns.n`)
	if err != nil {
		return err
	}
	type conversion struct {
		id, roomID string
		n          int
		raw        []byte
		cutoff     int64
	}
	var pending []conversion
	for rows.Next() {
		var c conversion
		if err := rows.Scan(&c.id, &c.n, &c.raw, &c.roomID); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	// Room readers may prepare their own projections. Read before taking the writer.
	rooms := &room.Store{Root: filepath.Join(filepath.Dir(s.root()), "rooms")}
	for i := range pending {
		c := &pending[i]
		var turn Turn
		if err := json.Unmarshal(c.raw, &turn); err != nil {
			return err
		}
		if c.roomID != "" && !turn.Ended.IsZero() {
			c.cutoff, err = rooms.OffsetAtTime(c.roomID, turn.Ended.UnixMilli())
			if err != nil {
				return fmt.Errorf("migrate turn cutoff %s/%d: %w", c.id, c.n, err)
			}
		}
	}
	return database.Migrate(db, "subagent.turns.cutoff", 1, func(conn *sql.Conn, _ int) error {
		for _, c := range pending {
			// Re-read inside the writer so prompt references and text belong
			// to the row being patched, including any concurrent update.
			raw, _, err := resolvedTurn(ctx, conn, c.id, c.n)
			if err != nil {
				return err
			}
			var normalized []byte
			if err := conn.QueryRowContext(ctx, "SELECT json_set(CAST(? AS TEXT),'$.inputCutoff',?)", raw, c.cutoff).Scan(&normalized); err != nil {
				return err
			}
			if _, err := saveTurnConn(ctx, conn, c.id, c.n, normalized); err != nil {
				return err
			}
		}
		return nil
	})
}
