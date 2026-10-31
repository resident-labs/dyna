package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"dyna-agent/internal/goals"
	"github.com/resident-labs/dyna/contract"
)

func readRules(db *sql.DB, room string, author func(string) (contract.ImportedAuthor, error)) ([]contract.Rule, error) {
	var body []byte
	if err := db.QueryRow("SELECT body FROM goal_rooms WHERE room=?", room).Scan(&body); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var set goals.RuleSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(set.Rules))
	for key := range set.Rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []contract.Rule
	for _, key := range keys {
		rule := set.Rules[key]
		if rule == nil {
			return nil, errors.New("rule is null")
		}
		by, err := author(rule.Setter)
		if err != nil {
			return nil, err
		}
		result = append(result, contract.Rule{Label: contract.RuleLabel(rule.ID), Text: rule.Text, By: by, Created: rule.CreatedAt, Updated: rule.UpdatedAt})
	}
	return result, nil
}
