package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type legacyDocuments struct {
	index   map[string]json.RawMessage
	records map[string]json.RawMessage
}

// convertLegacyQuota adds converted entries without encoding existing scoped entries.
func convertLegacyQuota(index map[string]json.RawMessage, disk diskIndex) error {
	if disk.LegacyRate == nil {
		return nil
	}
	disk.State.ImportLegacyQuota(*disk.LegacyRate)
	var quotas map[string]json.RawMessage
	if raw := index["rateLimits"]; raw != nil {
		if err := json.Unmarshal(raw, &quotas); err != nil {
			return err
		}
	}
	if quotas == nil {
		quotas = map[string]json.RawMessage{}
	}
	for key, rate := range disk.RateLimits {
		if _, found := quotas[key]; found {
			continue
		}
		raw, err := json.Marshal(rate)
		if err != nil {
			return err
		}
		quotas[key] = raw
	}
	raw, err := json.Marshal(quotas)
	if err != nil {
		return err
	}
	index["rateLimits"] = raw
	if !disk.LegacyPause.IsZero() {
		raw, err := json.Marshal(disk.LegacyPause)
		if err != nil {
			return err
		}
		index["legacyQuotaPause"] = raw
	}
	return nil
}

// importLegacyRows validates known fields and retains the complete documents.
func importLegacyRows(conn *sql.Conn, docs legacyDocuments) error {
	ctx := context.Background()
	for key, body := range docs.records {
		var row diskRecord
		if err := json.Unmarshal(body, &row); err != nil {
			return fmt.Errorf("invalid PR record %s: %w", key, err)
		}
		r := row.Record
		room, repo, number, closed := "", "", 0, false
		if r != nil {
			room, repo, number = r.RoomID, r.Repo, r.Number
			closed = r.State == "closed" || r.State == "merged"
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
