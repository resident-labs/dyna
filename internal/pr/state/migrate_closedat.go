package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// migrateClosedAt stores historical end times once, preserving fields that this
// version does not know. The migration transaction also advances snapshot revisions.
func migrateClosedAt(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, "SELECT key,body FROM pr_records")
	if err != nil {
		return err
	}
	type change struct{ key, body string }
	var changes []change
	for rows.Next() {
		var key string
		var body []byte
		if err := rows.Scan(&key, &body); err != nil {
			rows.Close()
			return err
		}
		var row diskRecord
		if err := json.Unmarshal(body, &row); err != nil {
			rows.Close()
			return fmt.Errorf("invalid PR record %s: %w", key, err)
		}
		r := row.Record
		if r == nil || (r.State != "closed" && r.State != "merged") || !r.ClosedAt.IsZero() {
			continue
		}
		at := r.Created
		if r.Landing != nil && r.Landing.State == "landed" && !r.Landing.At.IsZero() {
			at = r.Landing.At
		} else if !r.Checked.IsZero() {
			at = r.Checked
		}
		if at.IsZero() {
			continue
		}
		var doc map[string]json.RawMessage
		var record map[string]json.RawMessage
		// The typed decode above validated both objects and the selected time.
		if err := json.Unmarshal(body, &doc); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(doc["record"], &record); err != nil {
			rows.Close()
			return err
		}
		record["closedAt"], _ = json.Marshal(at)
		doc["record"], _ = json.Marshal(record)
		updated, err := json.Marshal(doc)
		if err != nil {
			rows.Close()
			return err
		}
		changes = append(changes, change{key, string(updated)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	var revision int64
	if err := conn.QueryRowContext(ctx, "SELECT revision+1 FROM pr_state WHERE id=1").Scan(&revision); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := conn.ExecContext(ctx, "UPDATE pr_records SET body=?,rev=? WHERE key=?", c.body, revision, c.key); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "UPDATE pr_state SET revision=? WHERE id=1", revision)
	return err
}
