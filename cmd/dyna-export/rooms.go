package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

// readRooms validates every name before a caller can use one as a path.
func readRooms(db *sql.DB) ([]room.Record, map[string]contract.RoomName, error) {
	rows, err := db.Query("SELECT id,body FROM room_records ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var records []room.Record
	byID := make(map[string]contract.RoomName)
	names := make(map[string]bool)
	for rows.Next() {
		var id string
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			return nil, nil, err
		}
		var rec room.Record
		if err := json.Unmarshal(body, &rec); err != nil {
			return nil, nil, fmt.Errorf("decode room: %w", err)
		}
		if rec.ID != id || room.ValidateName(rec.Name) != nil || names[rec.Name] {
			return nil, nil, fmt.Errorf("unsafe or duplicate room name, or mismatched room ID")
		}
		names[rec.Name] = true
		byID[id] = contract.RoomName(rec.Name)
		records = append(records, rec)
	}
	return records, byID, rows.Err()
}
