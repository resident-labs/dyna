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
		rows, err := conn.QueryContext(context.Background(), "SELECT id,meta,"+instructionReadProjection+" FROM session_records ORDER BY id")
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
			var instructions instructionRead
			if err := rows.Scan(&id, &raw, &instructions.hashes[0], &instructions.hashes[1], &instructions.bodies[0], &instructions.bodies[1]); err != nil {
				rows.Close()
				return err
			}
			joined, err := instructions.join([]byte(raw))
			if err != nil {
				rows.Close()
				return err
			}
			raw = string(joined)
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
				// TODO: legacy/shim — merge-steward and keeper are roles saved before
				// those services were removed; keep them marked as services so their
				// stored sessions migrate cleanly. Remove each name when no stored
				// session carries that role anymore.
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
