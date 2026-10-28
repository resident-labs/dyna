package sessionlife

import (
	"context"
	"database/sql"
	"dyna-agent/internal/database"
	"encoding/json"
)

func prepareHistory(db *sql.DB) error {
	return database.Migrate(db, "sessions.history-schema", 1, func(conn *sql.Conn, _ int) error {
		rows, err := conn.QueryContext(context.Background(), "PRAGMA table_info(session_records)")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			found = found || name == "history_only"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !found {
			if _, err := conn.ExecContext(context.Background(), "ALTER TABLE session_records ADD COLUMN history_only INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(context.Background(), "CREATE INDEX IF NOT EXISTS session_records_routing ON session_records(history_only, room_id, closed)")
		return err
	})
}

// TODO: legacy/shim remove later — classify retained pre-room sessions once.
func migrateHistory(db *sql.DB) error {
	return database.Migrate(db, "sessions.history", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		rows, err := conn.QueryContext(ctx, "SELECT id, meta FROM session_records")
		if err != nil {
			return err
		}
		type record struct {
			id  string
			raw []byte
		}
		var records []record
		for rows.Next() {
			var r record
			if err := rows.Scan(&r.id, &r.raw); err != nil {
				rows.Close()
				return err
			}
			records = append(records, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, r := range records {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(r.raw, &fields); err != nil {
				return err
			}
			_, hasID := fields["roomId"]
			_, hasRoom := fields["room"]
			if hasID || hasRoom {
				continue
			}
			fields["historyOnly"] = json.RawMessage("true")
			raw, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			if err := writeRow(conn, r.id, raw, "ON CONFLICT(id) DO UPDATE SET revision=excluded.revision, history_only=excluded.history_only, meta=excluded.meta, dir=excluded.dir, service=excluded.service, meta_id=excluded.meta_id, meta_digest=excluded.meta_digest"); err != nil {
				return err
			}
		}
		return nil
	})
}
