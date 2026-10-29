package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type legacyDocuments struct {
	source  string
	index   map[string]json.RawMessage
	records map[string]json.RawMessage
}

// importLegacyRows validates known fields and retains the complete documents.
func importLegacyRows(conn *sql.Conn, docs legacyDocuments) error {
	ctx := context.Background()
	for key, body := range docs.records {
		var row diskRecord
		if err := json.Unmarshal(body, &row); err != nil {
			return fmt.Errorf("invalid PR record %s in %s: %w", key, docs.source, err)
		}
		r := row.Record
		room, repo, number, closed := "", "", 0, false
		if r != nil {
			room, repo, number = r.RoomID, r.Repo, r.Number
			closed = r.State == PRClosed || r.State == PRMerged
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO pr_records(key,room,repo,number,closed,rev,body) VALUES(?,?,?,?,?,1,?)`, key, room, repo, number, closed, string(body)); err != nil {
			return err
		}
	}
	body, err := json.Marshal(docs.index)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO pr_state(id,revision,body) VALUES(1,1,?)", string(body))
	return err
}
