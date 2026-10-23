package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/database"
)

// TODO: legacy/shim remove later — persist service classification for retained
// sessions once, before readers rely only on the stored flag.
func migrateService(db *sql.DB) error {
	return database.Migrate(db, "sessions.service", 1, func(conn *sql.Conn, _ int) error {
		rows, err := conn.QueryContext(context.Background(), "SELECT id, meta FROM session_records ORDER BY id")
		if err != nil {
			return err
		}
		type record struct {
			fields        map[string]json.RawMessage
			Role          string
			ParentSession string
			RoomID        string
			Service       bool
		}
		records := map[string]record{}
		var ids []string
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var r record
			if err := json.Unmarshal([]byte(raw), &r); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal([]byte(raw), &r.fields); err != nil || r.fields == nil {
				rows.Close()
				return fmt.Errorf("%w: session %s service metadata is not an object", ErrRecord, id)
			}
			records[id] = r
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range ids {
			original := records[id]
			if original.Service {
				continue
			}
			ancestorID := id
			seen := map[string]bool{}
			for {
				if seen[ancestorID] {
					break
				}
				seen[ancestorID] = true
				ancestor, ok := records[ancestorID]
				if !ok || ancestor.RoomID != original.RoomID {
					break
				}
				// A root is a service, but its ordinary helpers are not.
				if ancestor.Role == "root" && ancestorID != id {
					break
				}
				service := ancestor.Service
				switch ancestor.Role {
				case "root", "reviewer", "keeper", "librarian", "merge-steward":
					service = true
				}
				if service {
					original.fields["service"] = json.RawMessage("true")
					if err := SaveTx(conn, id, original.fields); err != nil {
						return err
					}
					break
				}
				if ancestor.ParentSession == "" {
					break
				}
				ancestorID = ancestor.ParentSession
			}
		}
		return nil
	})
}
