package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/hooks"
	"github.com/resident-labs/dyna/contract"
)

type hookGap struct {
	Repo   string
	Name   string
	Fields []string
}

func readHooks(db *sql.DB, repoRooms map[contract.RepoName][]contract.RoomName, allRooms []contract.RoomName) (map[contract.RoomName][]contract.HookHandoff, []hookGap, error) {
	rows, err := db.Query("SELECT repo,body FROM hook_documents ORDER BY repo")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	handoffs := make(map[contract.RoomName][]contract.HookHandoff)
	var gaps []hookGap
	for rows.Next() {
		var repo string
		var body []byte
		if err := rows.Scan(&repo, &body); err != nil {
			return nil, nil, err
		}
		var doc struct {
			Hooks []hooks.Hook `json:"hooks"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, nil, fmt.Errorf("decode hook document: %w", err)
		}
		targets := repoRooms[contract.RepoName(repo)]
		if repo == "global" {
			targets = allRooms
		}
		for _, hook := range doc.Hooks {
			if hook.Disabled || hook.Builtin != "" {
				continue
			}
			// v1 hooks always execute a shell command; the typed Wake form
			// cannot faithfully represent an arbitrary shell command.
			fields := []string{"Event", "Command", "Cwd", "Timeout", "OnFail"}
			gaps = append(gaps, hookGap{Repo: repo, Name: hook.Name, Fields: fields})
			for _, target := range targets {
				handoffs[target] = append(handoffs[target], contract.HookHandoff{Name: hook.Name, Fields: fields})
			}
		}
	}
	return handoffs, gaps, rows.Err()
}
