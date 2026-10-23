package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/database"
)

// migrateDeliveryReasons normalizes historical interruption outcomes once.
// It preserves event time, wait duration, order and unknown record fields.
func (s *Store) migrateDeliveryReasons() error {
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	return database.Migrate(db, "subagent.turns.delivery_reason", 1, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		rows, err := conn.QueryContext(ctx, "SELECT session,n,turn FROM session_turns ORDER BY session,n")
		if err != nil {
			return err
		}
		type pendingTurn struct {
			id  string
			n   int
			raw []byte
		}
		var pending []pendingTurn
		for rows.Next() {
			var id string
			var n int
			var raw []byte
			if err := rows.Scan(&id, &n, &raw); err != nil {
				rows.Close()
				return err
			}
			normalized, changed, err := normalizeDeliveryReasons(raw)
			if err != nil {
				rows.Close()
				return fmt.Errorf("session %s turn %d delivery reasons: %w", id, n, err)
			}
			if changed {
				pending = append(pending, pendingTurn{id, n, normalized})
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, turn := range pending {
			if _, err := saveTurnConn(ctx, conn, turn.id, turn.n, turn.raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// TODO: legacy/shim remove later — converts retained pre-reason delivery outcomes.
func normalizeDeliveryReasons(raw []byte) ([]byte, bool, error) {
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, false, err
	}
	if record == nil {
		return nil, false, fmt.Errorf("turn is not an object")
	}
	encoded, exists := record["deliveries"]
	if !exists || string(encoded) == "null" {
		return raw, false, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(encoded, &entries); err != nil {
		return nil, false, fmt.Errorf("deliveries is not an array: %w", err)
	}
	changed := false
	for i, entry := range entries {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry, &fields); err != nil {
			return nil, false, fmt.Errorf("delivery %d is not an object: %w", i, err)
		}
		if fields == nil {
			return nil, false, fmt.Errorf("delivery %d is not an object", i)
		}
		var outcome string
		if value, exists := fields["outcome"]; exists {
			if err := json.Unmarshal(value, &outcome); err != nil {
				return nil, false, fmt.Errorf("delivery %d outcome: %w", i, err)
			}
		}
		var reason string
		switch outcome {
		case "bound-hit":
			reason = "wait-bound"
		case "unsupported":
			reason = "no-tool-boundaries"
		default:
			continue
		}
		fields["outcome"], _ = json.Marshal("interrupted")
		fields["reason"], _ = json.Marshal(reason)
		var err error
		entries[i], err = json.Marshal(fields)
		if err != nil {
			return nil, false, err
		}
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	var err error
	record["deliveries"], err = json.Marshal(entries)
	if err != nil {
		return nil, false, err
	}
	normalized, err := json.Marshal(record)
	return normalized, true, err
}
