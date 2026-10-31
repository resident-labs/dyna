package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

// These count queries are independent of exporter selection and decoding.
// V1 hooks execute shell commands. None can supply a typed Run or Wake
// subscription without inventing data, so their SQL eligibility set is empty.
func countRoomSQL(db *sql.DB, roomID string) (roomSQLCounts, error) {
	var c roomSQLCounts
	err := db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM goals WHERE room=? AND state='active'),
 (SELECT COUNT(*) FROM goal_items i JOIN goals g ON g.room=i.room AND g.id=i.goal WHERE i.room=? AND g.state='active'),
 (SELECT COUNT(*) FROM room_issues WHERE room_id=? AND state IN ('open','goal')),
 (SELECT COUNT(*) FROM questions WHERE room=? AND state='open'),
 (SELECT COUNT(*) FROM pr_rows WHERE room=? AND closed=0 AND lower(json_extract(body,'$.record.host'))='github.com'),
 (SELECT COUNT(*) FROM session_records WHERE room_id=? AND closed=0 AND history_only=0 AND service=0 AND json_extract(meta,'$.run') IS NULL),
 (SELECT COUNT(*) FROM hook_documents, json_each(hook_documents.body,'$.hooks') WHERE 0)`,
		roomID, roomID, roomID, roomID, roomID, roomID).Scan(&c.Goals, &c.GoalItems, &c.Issues, &c.Questions, &c.PRs, &c.Sessions, &c.Subscriptions)
	if err != nil {
		return c, fmt.Errorf("count room records: %w", err)
	}
	return c, nil
}

type roomSQLCounts struct {
	Goals, GoalItems, Issues, Questions, PRs, Sessions, Subscriptions int
}

func countGlobalSQL(db *sql.DB, cap capture) (contract.BundleCounts, error) {
	// Classify scope strings separately; SQL still selects and counts accounts.
	rows, err := db.Query(`SELECT DISTINCT s.repo FROM account_repo_scopes s JOIN accounts a ON a.id=s.account_id WHERE json_extract(a.metadata,'$.provider') IN ('anthropic','chatgpt','kimi-code','xai','zai','opencode-go','deepseek')`)
	if err != nil {
		return contract.BundleCounts{}, err
	}
	supported := []string{}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return contract.BundleCounts{}, err
		}
		_, err := repoName(cap.Repos, scope)
		if errors.Is(err, errUnsupportedForge) {
			continue
		}
		if err != nil {
			rows.Close()
			return contract.BundleCounts{}, err
		}
		supported = append(supported, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return contract.BundleCounts{}, err
	}
	rows.Close()
	scopes, err := json.Marshal(supported)
	if err != nil {
		return contract.BundleCounts{}, err
	}
	var c contract.BundleCounts
	err = db.QueryRow(`SELECT
 (SELECT COUNT(*) FROM machines WHERE enabled=1 AND local=0),
 (SELECT COUNT(*) FROM accounts a WHERE json_extract(a.metadata,'$.provider') IN ('anthropic','chatgpt','kimi-code','xai','zai','opencode-go','deepseek') AND NOT EXISTS (SELECT 1 FROM account_repo_scopes s WHERE s.account_id=a.id AND s.repo NOT IN (SELECT value FROM json_each(?)))),
 (SELECT COUNT(*) FROM web_devices),
 (SELECT COUNT(*) FROM hook_documents, json_each(hook_documents.body,'$.hooks') WHERE 0)`, string(scopes)).Scan(&c.Hosts, &c.Accounts, &c.Devices, &c.Subscriptions)
	if err != nil {
		return c, fmt.Errorf("count global records: %w", err)
	}
	return c, nil
}
