package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/goals"
	"github.com/resident-labs/dyna/contract"
)

// readGoals uses the v1 record types, but does not call the v1 store. Store
// opens run migrations and must never be used on the frozen input.
func readGoals(db *sql.DB, room string, author func(string) (contract.ImportedAuthor, error)) ([]contract.GoalBundle, error) {
	rows, err := db.Query("SELECT body FROM goals WHERE room=? AND state='active' ORDER BY id", room)
	if err != nil {
		return nil, err
	}
	var result []contract.GoalBundle
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		var g goals.Goal
		if err := json.Unmarshal(body, &g); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode goal: %w", err)
		}
		setter, err := author(g.Setter)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out := contract.GoalBundle{Label: contract.GoalID(g.ID), Text: g.Text, Detail: g.Detail, Setter: setter, Created: g.CreatedAt}
		for _, a := range g.Amendments {
			by, err := author(a.By)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out.Amendments = append(out.Amendments, contract.GoalNote{At: a.At, By: by, Text: a.Text})
		}
		result = append(result, out)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range result {
		items, err := db.Query("SELECT id,text,state,assignee FROM goal_items WHERE room=? AND goal=? ORDER BY id", room, result[i].Label)
		if err != nil {
			return nil, err
		}
		for items.Next() {
			var id, text, state, assignee string
			if err := items.Scan(&id, &text, &state, &assignee); err != nil {
				items.Close()
				return nil, err
			}
			var address contract.V1Address
			if assignee != "" {
				resolved, err := author(assignee)
				if err != nil {
					items.Close()
					return nil, err
				}
				address = resolved.Label
			}
			result[i].Items = append(result[i].Items, contract.GoalItemBundle{Label: id, Text: text, State: contract.ItemState(state), Assignee: address})
		}
		if err := items.Err(); err != nil {
			items.Close()
			return nil, err
		}
		items.Close()
	}
	return result, nil
}
