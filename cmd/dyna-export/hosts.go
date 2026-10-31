package main

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

func readHosts(db *sql.DB, rooms map[string]contract.RoomName) ([]contract.HostBundle, []string, error) {
	rows, err := db.Query("SELECT name,ssh,enabled,local,labels,rooms FROM machines ORDER BY name")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var hosts []contract.HostBundle
	var skipped []string
	for rows.Next() {
		var name, ssh, labelsJSON, roomsJSON string
		var enabled, local bool
		if err := rows.Scan(&name, &ssh, &enabled, &local, &labelsJSON, &roomsJSON); err != nil {
			return nil, nil, err
		}
		if !enabled || local {
			skipped = append(skipped, name)
			continue
		}
		var labels, roomIDs []string
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(roomsJSON), &roomIDs); err != nil {
			return nil, nil, err
		}
		host := contract.HostBundle{Name: contract.HostName(name), SSH: ssh, Labels: labels}
		for _, id := range roomIDs {
			room, ok := rooms[id]
			if !ok {
				return nil, nil, fmt.Errorf("host refers to unknown room")
			}
			host.Rooms = append(host.Rooms, room)
		}
		hosts = append(hosts, host)
	}
	return hosts, skipped, rows.Err()
}
