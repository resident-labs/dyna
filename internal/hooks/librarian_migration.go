package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// removeLibrarianHooks removes obsolete engine-owned repository hooks once.
// Raw fields preserve settings that a newer release may have stored.
func removeLibrarianHooks(conn *sql.Conn) error {
	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, "SELECT repo, body FROM hook_documents WHERE repo <> ?", globalKey)
	if err != nil {
		return err
	}
	type row struct {
		key  string
		body []byte
	}
	var docs []row
	for rows.Next() {
		var d row
		if err := rows.Scan(&d.key, &d.body); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, d := range docs {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(d.body, &doc); err != nil {
			return err
		}
		var list []json.RawMessage
		if raw := doc["hooks"]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &list); err != nil {
				return err
			}
		}
		kept := make([]json.RawMessage, 0, len(list))
		for _, raw := range list {
			var h Hook
			if err := json.Unmarshal(raw, &h); err != nil {
				return err
			}
			legacy := (h.Name == "librarian-merged" || h.Name == "librarian-batch") && strings.Contains(h.Command, " library wake --repo ")
			if h.Name != "librarian-main" && !legacy {
				kept = append(kept, raw)
			}
		}
		if len(kept) == len(list) {
			continue
		}
		doc["hooks"], err = json.Marshal(kept)
		if err != nil {
			return err
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "UPDATE hook_documents SET body=? WHERE repo=?", body, d.key); err != nil {
			return err
		}
	}
	return nil
}
