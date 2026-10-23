package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func retireWallBudget(record map[string]json.RawMessage) (bool, error) {
	var r Record
	raw, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return false, err
	}
	runs, notes := map[string]string{}, map[string]string{}
	remainingFailure := false
	reset := false
	for id, verdict := range r.CIBudgetRuns {
		if !strings.HasPrefix(verdict, "over CI time budget") {
			continue
		}
		if r.CIBudgetSteps[id] {
			remainingFailure = true
			continue
		}
		runs[id] = verdict
		if note, ok := r.CIBudgetNotes[id]; ok {
			notes[id] = note
		}
		if r.CIReason == verdict {
			reset = true
		}
		for _, field := range []string{"ciBudgetRuns", "ciBudgetNotes", "ciBudgetInputs", "ciBudgetSteps"} {
			var values map[string]json.RawMessage
			if err := json.Unmarshal(record[field], &values); err != nil && record[field] != nil {
				return false, err
			}
			delete(values, id)
			if len(values) == 0 {
				delete(record, field)
			} else {
				record[field], _ = json.Marshal(values)
			}
		}
	}
	if len(runs) == 0 {
		return false, nil
	}
	archive, _ := json.Marshal(map[string]any{"head": r.CIBudgetHead, "runs": runs, "notes": notes})
	history := r.ReviewHistory
	history = append(history, map[string]json.RawMessage{"ciBudget": archive})
	record["reviewHistory"], _ = json.Marshal(history)
	if reset && !remainingFailure && r.Head == r.CIBudgetHead && r.CI == "failed" {
		record["ci"] = json.RawMessage(`"pending"`)
		delete(record, "ciReason")
	}
	return true, nil
}

func migrateWallBudget(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	var indexRaw []byte
	var revision int64
	if err := conn.QueryRowContext(ctx, "SELECT body,revision FROM pr_state WHERE id=1").Scan(&indexRaw, &revision); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
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
		changed, err := retireWallBudget(record)
		if err != nil {
			rows.Close()
			return err
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
	if len(changes) == 0 {
		return nil
	}
	revision++
	for _, c := range changes {
		if _, err := conn.ExecContext(ctx, "UPDATE pr_records SET body=?,rev=? WHERE key=?", string(c.body), revision, c.key); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "UPDATE pr_state SET body=?,revision=? WHERE id=1", string(indexRaw), revision)
	return err
}
