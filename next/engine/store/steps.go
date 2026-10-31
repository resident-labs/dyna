package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 13

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
		`CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), to_kind TEXT NOT NULL, to_id TEXT NOT NULL, mode TEXT NOT NULL, expires_at INTEGER, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER, expired_at INTEGER);`,
		`CREATE TABLE grant_hosts (grant_id TEXT NOT NULL REFERENCES grants(id), host TEXT NOT NULL, PRIMARY KEY(grant_id, host));`,
		`CREATE TABLE grant_commands (grant_id TEXT NOT NULL REFERENCES grants(id), idx INTEGER NOT NULL, program TEXT NOT NULL, prefix INTEGER NOT NULL, PRIMARY KEY(grant_id, idx));`,
		`CREATE TABLE grant_command_args (grant_id TEXT NOT NULL, idx INTEGER NOT NULL, pos INTEGER NOT NULL, arg TEXT NOT NULL, PRIMARY KEY(grant_id, idx, pos), FOREIGN KEY(grant_id, idx) REFERENCES grant_commands(grant_id, idx));`,
		`INSERT INTO grants(id,kind,room,to_kind,to_id,mode,by_kind,by_id,created_at) SELECT 'grant_'||id,'hostexec',room,'session',id,'preapproved','engine','',created_at FROM sessions WHERE parent IS NULL AND closed_at IS NULL;`,
		`INSERT INTO grant_hosts(grant_id,host) SELECT 'grant_'||id,'*' FROM sessions WHERE parent IS NULL AND closed_at IS NULL;`,
		`INSERT INTO grant_commands(grant_id,idx,program,prefix) SELECT 'grant_'||id,0,'*',1 FROM sessions WHERE parent IS NULL AND closed_at IS NULL;`,
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
