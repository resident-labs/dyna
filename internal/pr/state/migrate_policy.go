package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
)

// retirePolicy edits only current policy fields; historical evidence stays raw.
func retirePolicy(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return raw, nil
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	changed := false
	for _, key := range []string{"land", "checks"} {
		if _, ok := p[key]; ok {
			delete(p, key)
			changed = true
		}
	}
	if checks, ok := p["requiredChecks"]; ok && !bytes.Equal(checks, []byte("null")) {
		var names []string
		if err := json.Unmarshal(checks, &names); err != nil {
			return nil, err
		}
		kept := make([]string, 0, len(names))
		for _, name := range names {
			if name == "go-test-touched" || name == "frontend-if-touched" {
				changed = true
			} else {
				kept = append(kept, name)
			}
		}
		if changed {
			p["requiredChecks"], _ = json.Marshal(kept)
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(p)
}

func migratePolicy(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	var indexRaw []byte
	var revision int64
	if err := conn.QueryRowContext(ctx, "SELECT body,revision FROM pr_state WHERE id=1").Scan(&indexRaw, &revision); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	var index map[string]json.RawMessage
	if err := json.Unmarshal(indexRaw, &index); err != nil {
		return err
	}
	indexChanged := false
	if raw, ok := index["policies"]; ok && string(raw) != "null" {
		var policies map[string]json.RawMessage
		if err := json.Unmarshal(raw, &policies); err != nil {
			return err
		}
		for room, p := range policies {
			next, err := retirePolicy(p)
			if err != nil {
				return err
			}
			if !bytes.Equal(next, p) {
				policies[room] = next
				indexChanged = true
			}
		}
		if indexChanged {
			index["policies"], _ = json.Marshal(policies)
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT key,body FROM pr_records")
	if err != nil {
		return err
	}
	type change struct {
		key  string
		body []byte
	}
	var changes []change
	for rows.Next() {
		var key string
		var body []byte
		if err := rows.Scan(&key, &body); err != nil {
			rows.Close()
			return err
		}
		var doc, record map[string]json.RawMessage
		if err := json.Unmarshal(body, &doc); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(doc["record"], &record); err != nil {
			rows.Close()
			return err
		}
		changed := false
		for _, field := range []string{"policyOverride"} {
			raw, ok := record[field]
			if !ok {
				continue
			}
			next, err := retirePolicy(raw)
			if err != nil {
				rows.Close()
				return err
			}
			if !bytes.Equal(next, raw) {
				record[field] = next
				changed = true
			}
		}
		if changed {
			doc["record"], _ = json.Marshal(record)
			next, err := json.Marshal(doc)
			if err != nil {
				rows.Close()
				return err
			}
			changes = append(changes, change{key, next})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !indexChanged && len(changes) == 0 {
		return nil
	}
	revision++
	for _, c := range changes {
		if _, err := conn.ExecContext(ctx, "UPDATE pr_records SET body=?,rev=? WHERE key=?", string(c.body), revision, c.key); err != nil {
			return err
		}
	}
	if indexChanged {
		indexRaw, err = json.Marshal(index)
		if err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "UPDATE pr_state SET body=?,revision=? WHERE id=1", string(indexRaw), revision)
	return err
}
