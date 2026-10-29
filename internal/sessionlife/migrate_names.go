package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/database"
)

// TODO: legacy/shim remove later — store the generated name of retained
// sessions that predate always-named sessions, once.
func migrateNames(db *sql.DB) error {
	return database.Migrate(db, "sessions.names", 1, func(conn *sql.Conn, _ int) error {
		rows, err := conn.QueryContext(context.Background(), "SELECT id,meta,"+instructionReadProjection+" FROM session_records WHERE name = '' ORDER BY id")
		if err != nil {
			return err
		}
		type record struct {
			id     string
			fields map[string]json.RawMessage
		}
		var pending []record
		for rows.Next() {
			var id, raw string
			instructions, err := scanInstructionRead(rows, &id, &raw)
			if err != nil {
				rows.Close()
				return err
			}
			joined, err := instructions.join([]byte(raw))
			if err != nil {
				rows.Close()
				return err
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(joined, &fields); err != nil || fields == nil {
				rows.Close()
				return fmt.Errorf("%w: session %s name metadata is not an object", ErrRecord, id)
			}
			name := id
			if len(name) > 8 {
				name = name[len(name)-8:]
			}
			fields["name"], _ = json.Marshal("agent-" + name)
			pending = append(pending, record{id, fields})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, r := range pending {
			if err := SaveTx(conn, r.id, r.fields); err != nil {
				return err
			}
		}
		return nil
	})
}
