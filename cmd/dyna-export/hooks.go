package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/hooks"
	"github.com/resident-labs/dyna/contract"
)

type hookGap struct {
	Repo               string
	Name               string
	Fields             []string
	UnverifiedCheckout bool
	WithoutRoom        bool
}

func readHooks(db *sql.DB, repoRooms map[contract.RepoName][]contract.RoomName, pathRooms map[string][]contract.RoomName, cap capture, allRooms []contract.RoomName) (map[contract.RoomName][]contract.HookHandoff, []hookGap, error) {
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
			Repo  string       `json:"repo"`
			Hooks []hooks.Hook `json:"hooks"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, nil, fmt.Errorf("decode hook document: %w", err)
		}
		identity := cap.Repos[doc.Repo]
		targets := repoRooms[identity]
		unverified := repo != "global" && identity == ""
		if unverified {
			targets = append([]contract.RoomName{}, pathRooms[doc.Repo]...)
			for _, name := range cap.HookRooms[doc.Repo] {
				found := false
				for _, r := range allRooms {
					if r == name {
						found = true
					}
				}
				if !found {
					return nil, nil, fmt.Errorf("hook manifest refers to unknown room")
				}
				duplicate := false
				for _, r := range targets {
					if r == name {
						duplicate = true
					}
				}
				if !duplicate {
					targets = append(targets, name)
				}
			}
		}
		if repo == "global" {
			targets = nil
		}
		for _, hook := range doc.Hooks {
			if hook.Disabled || hook.Builtin != "" {
				continue
			}
			// v1 hooks always execute a shell command; the typed Wake form
			// cannot faithfully represent an arbitrary shell command.
			fields := []string{"Label", "Event", "Command", "Cwd", "Timeout", "OnFail", "Scope", "AddedBy", "AddedAt", "EditedBy", "EditedAt", "EveryMinutes"}
			gaps = append(gaps, hookGap{Repo: repo, Name: hook.Name, Fields: fields, UnverifiedCheckout: unverified, WithoutRoom: len(targets) == 0})
			for _, target := range targets {
				handoff := contract.HookHandoff{Name: hook.Name, Fields: fields}
				if unverified {
					handoff.Reason = contract.HookCheckoutUnverified
				}
				handoffs[target] = append(handoffs[target], handoff)
			}
		}
	}
	return handoffs, gaps, rows.Err()
}
