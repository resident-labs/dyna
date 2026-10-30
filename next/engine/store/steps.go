package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

const schemaVersion = 3

// upgrade applies each future numbered step in the same transaction as its version.
func upgrade(db *sql.DB, floor int, commit string) error {
	var value string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil {
		return err
	}
	version, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid schema version: %w", err)
	}
	if version < floor {
		return &Error{Code: StoreTooOld, Message: fmt.Sprintf("This store is too old. Update through commit %s first.", commit)}
	}
	if version == 1 {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		var definition string
		for _, statement := range strings.Split(schema, ";") {
			if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE deliveries ") {
				definition = strings.Replace(strings.TrimSpace(statement), "CREATE TABLE deliveries ", "CREATE TABLE deliveries_new ", 1)
				break
			}
		}
		for _, statement := range []string{
			definition,
			"INSERT INTO deliveries_new SELECT *, NULL FROM deliveries",
			"DROP TABLE deliveries",
			"ALTER TABLE deliveries_new RENAME TO deliveries",
		} {
			if _, err = tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if _, err = tx.Exec("UPDATE meta SET value='2' WHERE key='schema_version'"); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, statement := range []string{
			"CREATE TABLE repos (repo TEXT PRIMARY KEY, default_room TEXT NOT NULL REFERENCES rooms(id))",
			"CREATE TABLE room_repos (room TEXT NOT NULL REFERENCES rooms(id), repo TEXT NOT NULL REFERENCES repos(repo), is_default INTEGER NOT NULL, PRIMARY KEY(room, repo))",
			"CREATE UNIQUE INDEX room_repos_default ON room_repos(room) WHERE is_default = 1",
			"UPDATE meta SET value='3' WHERE key='schema_version'",
		} {
			if _, err = tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version = 3
	}
	if version != schemaVersion {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	return nil
}
