package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 9

// Every schema change adds an entry here and updates schema.sql.
var steps = map[int][]string{
	5: {
		`CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB);`,
		`CREATE UNIQUE INDEX operations_pending_dedup ON operations(room, by_kind, by_id, kind, args_sha256) WHERE state = 'pending';`,
	},
	6: {"CREATE TABLE transcript_offsets (session TEXT NOT NULL REFERENCES sessions(id), path TEXT NOT NULL, acked INTEGER NOT NULL, PRIMARY KEY(session, path))"},
	7: {"CREATE TABLE shell_jobs (session TEXT NOT NULL REFERENCES sessions(id), job TEXT NOT NULL, PRIMARY KEY(session, job))"},
	8: {"CREATE TABLE event_files (seq INTEGER NOT NULL REFERENCES events(seq), sha256 TEXT NOT NULL, media TEXT NOT NULL, PRIMARY KEY(sha256, seq))"},
}

// upgrade applies each future numbered step in the same transaction as its version.
func upgrade(db *sql.DB, floor int, commit string, steps map[int][]string) error {
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
	for ; version < schemaVersion; version++ {
		statements, ok := steps[version]
		if !ok {
			return fmt.Errorf("missing schema step %d to %d", version, version+1)
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, statement := range append(statements, fmt.Sprintf("UPDATE meta SET value='%d' WHERE key='schema_version'", version+1)) {
			if _, err := tx.Exec(statement); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if version != schemaVersion {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	return nil
}
