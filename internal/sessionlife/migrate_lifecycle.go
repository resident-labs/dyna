package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/database"
)

// TODO: legacy/shim remove later — persist lifecycle fields for historical session records.
func migrateLifecycle(db *sql.DB) error {
	return database.Migrate(db, "sessions.lifecycle", 1, func(conn *sql.Conn, _ int) error {
		rows, err := conn.QueryContext(context.Background(), "SELECT id, meta FROM session_records WHERE json_type(meta,'$.done') IS NOT NULL OR status = ''")
		if err != nil {
			return err
		}
		type record struct {
			id     string
			fields map[string]json.RawMessage
		}
		var records []record
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var fields map[string]json.RawMessage
			var lifecycle struct {
				Closed   time.Time
				Finished time.Time
				Status   string
			}
			if err := json.Unmarshal([]byte(raw), &fields); err != nil {
				rows.Close()
				return fmt.Errorf("%w: lifecycle session %s: %v", ErrRecord, id, err)
			}
			if fields == nil {
				rows.Close()
				return fmt.Errorf("%w: lifecycle session %s is not an object", ErrRecord, id)
			}
			if err := json.Unmarshal([]byte(raw), &lifecycle); err != nil {
				rows.Close()
				return err
			}
			var done bool
			_ = json.Unmarshal(fields["done"], &done)
			if done {
				if !lifecycle.Closed.IsZero() {
					fields["finished"] = fields["closed"]
					lifecycle.Finished = lifecycle.Closed
				}
				delete(fields, "closed")
				lifecycle.Closed = time.Time{}
				if !lifecycle.Finished.IsZero() {
					lifecycle.Status = "dormant"
				}
			}
			delete(fields, "done")
			if !lifecycle.Closed.IsZero() {
				lifecycle.Status = "closed"
			}
			if lifecycle.Status == "" {
				lifecycle.Status = "idle"
				if !lifecycle.Finished.IsZero() {
					lifecycle.Status = "dormant"
				}
			}
			fields["status"], _ = json.Marshal(lifecycle.Status)
			records = append(records, record{id, fields})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, record := range records {
			if err := SaveTx(conn, record.id, record.fields); err != nil {
				return err
			}
		}
		return nil
	})
}
