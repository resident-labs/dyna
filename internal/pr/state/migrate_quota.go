package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// TODO: legacy/shim remove later — replace old global pauses with recorded scopes.
func migrateQuota(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	var body []byte
	if err := conn.QueryRowContext(ctx, "SELECT body FROM pr_state WHERE id=1").Scan(&body); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}
	legacy, mixed := doc["legacyQuotaPause"], doc["rateLimit"]
	if legacy == nil && mixed == nil {
		return nil
	}
	var until time.Time
	if legacy != nil {
		if err := json.Unmarshal(legacy, &until); err != nil {
			return err
		}
	}
	var rate RateLimit
	if mixed != nil {
		if err := json.Unmarshal(mixed, &rate); err != nil {
			return err
		}
	}
	rates := map[string]json.RawMessage{}
	if raw := doc["rateLimits"]; raw != nil {
		if err := json.Unmarshal(raw, &rates); err != nil {
			return err
		}
	}
	if rates == nil {
		rates = map[string]json.RawMessage{}
	}
	scopes := map[RateLimitScope]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT body FROM pr_records")
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var disk diskRecord
		if err = json.Unmarshal(raw, &disk); err != nil {
			rows.Close()
			return err
		}
		if disk.Record == nil {
			continue
		}
		for _, resource := range []string{"core", "graphql"} {
			scope := (RateLimitScope{disk.Record.Host, disk.Record.QuotaAccount, resource}).Normalize()
			if scope.Known() {
				scopes[scope] = true
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	patch := func(scope RateLimitScope, pause time.Time, reason string) error {
		if pause.IsZero() {
			return nil
		}
		fields := map[string]json.RawMessage{}
		if raw := rates[scope.Key()]; raw != nil {
			if err := json.Unmarshal(raw, &fields); err != nil {
				return err
			}
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		var old time.Time
		if raw := fields["pauseUntil"]; raw != nil {
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
		}
		if !pause.After(old) {
			return nil
		}
		fields["pauseUntil"], _ = json.Marshal(pause)
		fields["reason"], _ = json.Marshal(reason)
		if fields["pauseScope"] == nil {
			fields["pauseScope"], _ = json.Marshal(scope)
		}
		raw, err := json.Marshal(fields)
		rates[scope.Key()] = raw
		return err
	}
	for scope := range scopes {
		if err := patch(scope, until, "GitHub rate limit"); err != nil {
			return err
		}
	}
	scope := rate.PauseScope.Normalize()
	if scope.Known() {
		if err := patch(scope, rate.PauseUntil, rate.Reason); err != nil {
			return err
		}
	} else {
		for scope := range scopes {
			if err := patch(scope, rate.PauseUntil, rate.Reason); err != nil {
				return err
			}
		}
	}
	// Keep unassignable evidence outside the live scheduling document.
	if len(scopes) == 0 && (!until.IsZero() || (!scope.Known() && !rate.PauseUntil.IsZero())) {
		if _, err := conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS pr_quota_archive (id INTEGER PRIMARY KEY CHECK(id=1), body TEXT NOT NULL CHECK(json_valid(body)))"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO pr_quota_archive VALUES(1,?)", string(body)); err != nil {
			return err
		}
	}
	delete(doc, "legacyQuotaPause")
	delete(doc, "rateLimit")
	doc["rateLimits"], err = json.Marshal(rates)
	if err != nil {
		return err
	}
	body, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "UPDATE pr_state SET body=?,revision=revision+1 WHERE id=1", string(body))
	return err
}
