package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

func readHandoff(db *sql.DB, roomID, roomName string, feed roomFeed, refs map[string]string, dropped []profileGap, author func(string) (contract.ImportedAuthor, error)) ([]contract.SessionHandoff, error) {
	rows, err := db.Query(`SELECT id,name,parent,meta FROM session_records
WHERE room_id=? AND closed=0 AND history_only=0 AND service=0 AND json_extract(meta,'$.run') IS NULL ORDER BY id`, roomID)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id, name, parent, profile string
	}
	var source []entry
	for rows.Next() {
		var e entry
		var body []byte
		if err := rows.Scan(&e.id, &e.name, &e.parent, &body); err != nil {
			rows.Close()
			return nil, err
		}
		var meta struct {
			Profile string `json:"profile"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode session metadata: %w", err)
		}
		e.profile = meta.Profile
		source = append(source, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var result []contract.SessionHandoff
	for _, e := range source {
		name := roomName + ":" + e.name
		handoff := contract.SessionHandoff{Name: contract.V1Address(name), Profile: e.profile, ArchiveRef: refs[e.id], LastPost: feed.lastPosts[name]}
		for _, gap := range dropped {
			if gap.Profile == e.profile {
				handoff.ProfileWarning = gap.Reason
				break
			}
		}
		if e.parent != "" {
			parent, err := author(e.parent)
			if err != nil {
				return nil, err
			}
			handoff.Parent = parent.Label
		}
		goals, err := db.Query(`SELECT g.id FROM goals g, json_each(g.body,'$.holders') h WHERE g.room=? AND g.state='active' AND h.value=? ORDER BY g.id`, roomID, e.id)
		if err != nil {
			return nil, err
		}
		for goals.Next() {
			var id string
			if err := goals.Scan(&id); err != nil {
				goals.Close()
				return nil, err
			}
			handoff.Goals = append(handoff.Goals, contract.GoalID(id))
		}
		if err := goals.Err(); err != nil {
			goals.Close()
			return nil, err
		}
		goals.Close()
		result = append(result, handoff)
	}
	return result, nil
}
