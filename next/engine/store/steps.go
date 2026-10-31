package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 14

// Every schema change adds an entry here and updates schema.sql.
var steps = map[int][]string{
	5: {
		`CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB);`,
		`CREATE UNIQUE INDEX operations_pending_dedup ON operations(room, by_kind, by_id, kind, args_sha256) WHERE state = 'pending';`,
	},
	6: {"CREATE TABLE transcript_offsets (session TEXT NOT NULL REFERENCES sessions(id), path TEXT NOT NULL, acked INTEGER NOT NULL, PRIMARY KEY(session, path))"},
	7: {"CREATE TABLE shell_jobs (session TEXT NOT NULL REFERENCES sessions(id), job TEXT NOT NULL, PRIMARY KEY(session, job))"},
	8: {"CREATE TABLE event_files (seq INTEGER NOT NULL REFERENCES events(seq), sha256 TEXT NOT NULL, media TEXT NOT NULL, PRIMARY KEY(sha256, seq))"},
	9: {`CREATE TABLE pr_owners (repo TEXT NOT NULL REFERENCES repos(repo), number INTEGER NOT NULL, session TEXT NOT NULL REFERENCES sessions(id), goal TEXT NOT NULL, approved_head TEXT NOT NULL, PRIMARY KEY(repo, number));`},
	10: {
		`CREATE INDEX turns_session_ended ON turns(session, ended_at)`,
		`CREATE INDEX sessions_room_closed ON sessions(room, closed_at)`,
		`CREATE INDEX sessions_parent ON sessions(parent)`,
		`CREATE INDEX operations_state_expires ON operations(state, expires_at)`,
	},
	11: {"ALTER TABLE turns ADD COLUMN shell_jobs INTEGER NOT NULL DEFAULT -1"},
	12: {
		`CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), to_kind TEXT NOT NULL, to_id TEXT NOT NULL, mode TEXT NOT NULL, hosts BLOB NOT NULL, commands BLOB NOT NULL, expires_at INTEGER, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER, expired_at INTEGER);`,
		`CREATE INDEX grants_expiry ON grants(expires_at) WHERE expired_at IS NULL AND revoked_at IS NULL;`,
		`CREATE INDEX grants_room_principal ON grants(room, to_id);`,
		`INSERT INTO grants(id,kind,room,to_kind,to_id,mode,hosts,commands,by_kind,by_id,created_at) SELECT 'grant_'||id,'hostexec',room,'session',id,'preapproved','["*"]','[{"program":"*","prefix":true}]','engine','',created_at FROM sessions WHERE parent IS NULL AND closed_at IS NULL;`,
	},
	13: {
		`CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY REFERENCES operations(id), grant_id TEXT NOT NULL REFERENCES grants(id), host TEXT NOT NULL, scope TEXT NOT NULL, started_at INTEGER NOT NULL, timeout_ms INTEGER NOT NULL, ended_at INTEGER, output_bytes INTEGER NOT NULL DEFAULT 0, stopped TEXT NOT NULL DEFAULT '');`,
		`CREATE INDEX hostexec_runs_grant ON hostexec_runs(grant_id, ended_at);`,
	},
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
