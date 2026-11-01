package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 54

// Every schema change adds an entry here and updates schema.sql.
var steps = map[int][]string{
	53: {`ALTER TABLE hosts ADD COLUMN disk_floor INTEGER NOT NULL DEFAULT 21474836480`, `CREATE TABLE disk_warnings (host TEXT PRIMARY KEY)`},
	52: {`CREATE INDEX turns_session_started_at ON turns(session, started_at DESC)`, `UPDATE turns SET sections=NULL WHERE ended_at IS NOT NULL AND sections IS NOT NULL`},
	51: {`CREATE TABLE client_replays (method TEXT NOT NULL, caller_kind TEXT NOT NULL, caller_id TEXT NOT NULL, client_id TEXT NOT NULL, ref TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(method, caller_kind, caller_id, client_id))`},
	50: {`ALTER TABLE connect_link_state ADD COLUMN generation INTEGER NOT NULL DEFAULT 0`, `CREATE TABLE connect_revocations (relay_device TEXT PRIMARY KEY, revoked_at INTEGER NOT NULL)`, `CREATE TABLE connect_devices (relay_device TEXT PRIMARY KEY, device TEXT NOT NULL UNIQUE REFERENCES devices(id))`, `CREATE TABLE connect_sessions (id TEXT PRIMARY KEY, relay_device TEXT NOT NULL REFERENCES connect_devices(relay_device), origin TEXT NOT NULL, jkt TEXT NOT NULL, user_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation INTEGER NOT NULL, token_hash BLOB NOT NULL UNIQUE, old1 BLOB, old2 BLOB, old3 BLOB, prev_until INTEGER NOT NULL, created_at INTEGER NOT NULL, access_until INTEGER NOT NULL, grant_end INTEGER NOT NULL, grant_at INTEGER NOT NULL)`},
	49: {`ALTER TABLE turns ADD COLUMN sections BLOB`, `CREATE INDEX pr_owners_session ON pr_owners(session)`, `CREATE INDEX operations_pending_caller ON operations(by_kind, by_id, created_at, id) WHERE state='pending'`},
	48: {`CREATE TABLE transcript_turns (session TEXT NOT NULL REFERENCES sessions(id), turn TEXT NOT NULL, path TEXT NOT NULL, start INTEGER NOT NULL, PRIMARY KEY(session, turn, path))`, `CREATE UNIQUE INDEX transcript_turns_path_start ON transcript_turns(session, path, start)`},
	47: {`CREATE TABLE connect_attempt (id INTEGER PRIMARY KEY CHECK(id=1), state TEXT NOT NULL, expires INTEGER NOT NULL)`, `CREATE TABLE connect_connector (id INTEGER PRIMARY KEY CHECK(id=1), missing INTEGER NOT NULL CHECK(missing IN (0,1)))`},
	46: {`CREATE TABLE connect_link_state (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL CHECK(epoch>=0), linked INTEGER NOT NULL CHECK(linked IN (0,1)))`, `CREATE TABLE connect_nonces (nonce TEXT PRIMARY KEY, expires_at INTEGER NOT NULL)`},
	45: {
		// Legacy rule events are retired with the rules feature. Remove dependent
		// rows first so foreign keys stay valid; retain all other event history.
		`DELETE FROM deliveries WHERE seq IN (SELECT seq FROM events WHERE type IN ('rule.added','rule.edited','rule.removed'))`,
		`DELETE FROM event_files WHERE seq IN (SELECT seq FROM events WHERE type IN ('rule.added','rule.edited','rule.removed'))`,
		`DELETE FROM events WHERE type IN ('rule.added','rule.edited','rule.removed')`,
		`DROP TABLE rules`,
	},
	44: {`CREATE INDEX turns_backfill_start ON events(json_extract(CAST(body AS TEXT),'$.turn')) WHERE type='turn.started'`, `UPDATE turns SET started_seq=(SELECT seq FROM events WHERE type='turn.started' AND json_extract(CAST(body AS TEXT),'$.turn')=turns.id) WHERE started_seq IS NULL`, `DROP INDEX turns_backfill_start`, `CREATE INDEX turns_session_started ON turns(session, started_seq)`, `CREATE INDEX events_sender_type ON events(room, from_kind, from_id, type, seq)`},
	43: {
		`CREATE TEMP TABLE hostexec_runs_backup AS SELECT * FROM hostexec_runs`,
		`DROP TABLE hostexec_runs`,
		`CREATE TABLE operations_new (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB)`,
		`INSERT INTO operations_new SELECT * FROM operations`,
		`DROP TABLE operations`,
		`ALTER TABLE operations_new RENAME TO operations`,
		`CREATE UNIQUE INDEX operations_pending_dedup ON operations(room, by_kind, by_id, kind, args_sha256) WHERE state = 'pending'`,
		`CREATE INDEX operations_state_expires ON operations(state, expires_at)`,
		`CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY REFERENCES operations(id), grant_id TEXT NOT NULL REFERENCES grants(id), host TEXT NOT NULL, scope TEXT NOT NULL, started_at INTEGER NOT NULL, timeout_ms INTEGER NOT NULL, ended_at INTEGER, output_bytes INTEGER NOT NULL DEFAULT 0, stopped TEXT NOT NULL DEFAULT '', lost_at INTEGER)`,
		`INSERT INTO hostexec_runs SELECT * FROM hostexec_runs_backup`,
		`CREATE INDEX hostexec_runs_grant ON hostexec_runs(grant_id, ended_at)`,
		`CREATE INDEX hostexec_runs_pending ON hostexec_runs(ended_at) WHERE ended_at IS NULL`,
		`DROP TABLE hostexec_runs_backup`,
	},
	42: {`CREATE TABLE proxy_auths (id TEXT PRIMARY KEY, provider TEXT NOT NULL, record BLOB NOT NULL, updated_at INTEGER NOT NULL)`},
}

// upgrade applies each future numbered step in the same transaction as its version.
func upgrade(db *sql.DB, floor int, commit string, steps map[int][]string, before func(int) error) error {
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
	if version < schemaVersion && before != nil {
		if err := before(version); err != nil {
			return fmt.Errorf("pre-upgrade backup: %w", err)
		}
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
