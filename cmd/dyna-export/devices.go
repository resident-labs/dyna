package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var deviceScopes = map[string]contract.DeviceScope{
	"read": "read", "operate": "operate", "files": "files",
	"terminal": "terminal", "computers": "computers", "admin": "admin",
}

func readDevices(db *sql.DB) ([]contract.DeviceBundle, []string, error) {
	rows, err := db.Query(`SELECT d.name,d.hash,d.created,d.expires,COALESCE(g.scopes,'[]')
FROM web_devices d LEFT JOIN device_grants g ON g.kind='paired' AND g.id=d.id ORDER BY d.id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var devices []contract.DeviceBundle
	var unknown []string
	for rows.Next() {
		var name, hash, created, expires, scopesJSON string
		if err := rows.Scan(&name, &hash, &created, &expires, &scopesJSON); err != nil {
			return nil, nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid device created time: %w", err)
		}
		until, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid device expiry: %w", err)
		}
		var scopes []string
		if err := json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
			return nil, nil, fmt.Errorf("invalid device scopes: %w", err)
		}
		device := contract.DeviceBundle{Name: name, TokenSHA256: hash, Created: at, Expires: until}
		for _, scope := range scopes {
			if mapped, ok := deviceScopes[scope]; ok {
				device.Scopes = append(device.Scopes, mapped)
			} else {
				unknown = append(unknown, scope)
			}
		}
		devices = append(devices, device)
	}
	return devices, unknown, rows.Err()
}
