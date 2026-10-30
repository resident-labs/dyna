package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/database"
	"dyna-agent/internal/workeroutput"
)

func (s *Store) migrateRetainedUsage() error {
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key='subagent.turns.usage'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version == "" || version == "0" {
		if err := database.BackUpOnce(db); err != nil {
			return fmt.Errorf("back up dyna.db before normalizing usage: %w", err)
		}
	}
	return database.Migrate(db, "subagent.turns.usage", 1, normalizeRetainedUsage)
}

// TODO: legacy/shim remove later — persist coverage and per-turn costs for
// retained turns before rebuilding usage accounting. Predecessors stay in
// their original form so converting one row cannot change the next delta.
func normalizeRetainedUsage(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	rows, err := conn.QueryContext(ctx, "SELECT session,n,turn FROM session_turns ORDER BY session,n")
	if err != nil {
		return err
	}
	type retained struct {
		id  string
		n   int
		raw []byte
	}
	var turns []retained
	for rows.Next() {
		var row retained
		if err := rows.Scan(&row.id, &row.n, &row.raw); err != nil {
			rows.Close()
			return err
		}
		turns = append(turns, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var previous Turn
	var previousID string
	var previousN int
	for _, row := range turns {
		var original Turn
		if err := json.Unmarshal(row.raw, &original); err != nil {
			return fmt.Errorf("normalize usage %s/%d: %w", row.id, row.n, err)
		}
		var prev *Turn
		if row.id == previousID && row.n == previousN+1 {
			prev = &previous
		}
		raw, err := normalizedUsageRow(row.raw, row.n, prev, original)
		if err != nil {
			return fmt.Errorf("normalize usage %s/%d: %w", row.id, row.n, err)
		}
		if !bytes.Equal(raw, row.raw) {
			resolved, err := resolveRowPrompts(ctx, conn, row.id, row.n, raw)
			if err != nil {
				return err
			}
			if _, err := saveTurnConn(ctx, conn, row.id, row.n, resolved); err != nil {
				return err
			}
		}
		previous, previousID, previousN = original, row.id, row.n
	}
	return nil
}

func legacyClaudeCost(t Turn) bool {
	n := t.UsageNormalization
	return t.OutputDialect == workeroutput.DialectClaudeStreamJSON && n.Version == 1 && n.Cost == workeroutput.CoverageComplete && n.CostBasis != workeroutput.CostBasisTurn && t.Usage.CostUSD >= 0
}

func normalizedUsageRow(raw []byte, n int, prev *Turn, t Turn) ([]byte, error) {
	norm := t.UsageNormalization
	unsupported := norm.Version != 1
	cumulative := t.OutputDialect == workeroutput.DialectClaudeStreamJSON && norm.CostBasis != workeroutput.CostBasisTurn
	if !unsupported && !cumulative {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var coverage map[string]json.RawMessage
	if err := json.Unmarshal(fields["usageNormalization"], &coverage); err != nil && len(fields["usageNormalization"]) != 0 {
		return nil, err
	}
	if coverage == nil {
		coverage = map[string]json.RawMessage{}
	}
	put := func(key string, value any) { coverage[key], _ = json.Marshal(value) }
	if unsupported {
		put("version", 1)
		put("input", workeroutput.CoverageUnknown)
		put("cachedInput", workeroutput.CoverageUnknown)
		put("output", workeroutput.CoverageUnknown)
		put("cost", workeroutput.CoverageUnknown)
		put("reason", "retained usage normalization unavailable")
	} else if cumulative {
		known := legacyClaudeCost(t) && (n == 1 || prev != nil && legacyClaudeCost(*prev) && t.HarnessSession != "" && t.HarnessSession == prev.HarnessSession && t.Usage.CostUSD >= prev.Usage.CostUSD)
		if known {
			cost := t.Usage.CostUSD
			if n != 1 {
				cost -= prev.Usage.CostUSD
			}
			var usage map[string]json.RawMessage
			if err := json.Unmarshal(fields["usage"], &usage); err != nil {
				return nil, err
			}
			usage["costUsd"], _ = json.Marshal(cost)
			fields["usage"], _ = json.Marshal(usage)
			put("costBasis", workeroutput.CostBasisTurn)
		} else {
			put("cost", workeroutput.CoverageUnknown)
			put("reason", "retained cumulative cost baseline unavailable")
		}
	}
	fields["usageNormalization"], _ = json.Marshal(coverage)
	return json.Marshal(fields)
}
