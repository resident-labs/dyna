package state

import (
	"context"
	"database/sql"
	"encoding/json"
)

// migratePolicyDefaults fills old policy documents without decoding opaque fields.
func migratePolicyDefaults(conn *sql.Conn, _ int) error {
	ctx := context.Background()
	var raw []byte
	if err := conn.QueryRowContext(ctx, "SELECT body FROM pr_state WHERE id=1").Scan(&raw); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	policies := map[string]json.RawMessage{}
	if value := doc["policies"]; len(value) > 0 {
		if err := json.Unmarshal(value, &policies); err != nil {
			return err
		}
	}
	if policies == nil {
		policies = map[string]json.RawMessage{}
	}
	var reviewers map[string]struct {
		Disabled bool `json:"disabled"`
	}
	if value := doc["reviewers"]; len(value) > 0 {
		if err := json.Unmarshal(value, &reviewers); err != nil {
			return err
		}
	}
	changed := false
	for room, reviewer := range reviewers {
		if _, exists := policies[room]; reviewer.Disabled && !exists {
			policies[room] = json.RawMessage(`{"review":"off","requiredChecks":[]}`)
			changed = true
		}
	}
	for room, raw := range policies {
		var policy map[string]json.RawMessage
		if err := json.Unmarshal(raw, &policy); err != nil {
			return err
		}
		if policy == nil {
			continue
		}
		patched := false
		for key, value := range map[string]string{"ciTimeBudget": `"warn"`, "ciWarnAfter": `"2m"`, "ciFailAfter": `"3m"`, "ciTestMax": `"10s"`, "ciNoJUnitWarning": "true"} {
			if old, exists := policy[key]; !exists || string(old) == "null" || (key != "ciNoJUnitWarning" && string(old) == `""`) {
				policy[key] = json.RawMessage(value)
				patched = true
			}
		}
		if patched {
			next, err := json.Marshal(policy)
			if err != nil {
				return err
			}
			policies[room] = next
			changed = true
		}
	}
	if !changed {
		return nil
	}
	var err error
	doc["policies"], err = json.Marshal(policies)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "UPDATE pr_state SET body=?,revision=revision+1 WHERE id=1", string(raw))
	return err
}
