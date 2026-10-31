package main

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// resolveAuthor keeps v1 labels as display text, never as v2 session IDs.
func resolveAuthor(db *sql.DB, value string) (contract.ImportedAuthor, error) {
	switch value {
	case "owner":
		return contract.ImportedAuthor{Kind: contract.PrincipalOwner, Label: "owner"}, nil
	case "engine":
		return contract.ImportedAuthor{Kind: contract.PrincipalEngine, Label: "engine"}, nil
	}
	var room, name string
	err := db.QueryRow("SELECT room,name FROM session_records WHERE id=?", value).Scan(&room, &name)
	if err == nil && room != "" && name != "" {
		return contract.ImportedAuthor{Kind: contract.PrincipalSession, Label: contract.V1Address(room + ":" + name)}, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return contract.ImportedAuthor{}, err
	}
	// Room addresses already identify their author by their v1 label.
	if strings.Count(value, ":") == 1 && !strings.HasPrefix(value, "ses_") {
		parts := strings.SplitN(value, ":", 2)
		if parts[0] != "" && parts[1] != "" {
			return contract.ImportedAuthor{Kind: contract.PrincipalSession, Label: contract.V1Address(value)}, nil
		}
	}
	return contract.ImportedAuthor{}, fmt.Errorf("cannot resolve author label")
}
