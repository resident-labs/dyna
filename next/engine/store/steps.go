package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 42

// Every schema change adds an entry here and updates schema.sql.
var steps = map[int][]string{
	40: {`CREATE TABLE visuals (id TEXT PRIMARY KEY, room TEXT NOT NULL, author_kind TEXT NOT NULL, author_id TEXT NOT NULL, version INTEGER NOT NULL)`},
	41: {`CREATE TABLE accounts (id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL UNIQUE, provider TEXT NOT NULL, label TEXT NOT NULL)`, `CREATE TABLE account_rooms (account TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, room TEXT NOT NULL REFERENCES rooms(id), PRIMARY KEY(account, room))`, `CREATE TABLE account_alerts (account TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, window TEXT NOT NULL, kind TEXT NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(account, window, kind))`},
	38: {`UPDATE operations SET state='running' WHERE state='approved'`},
	39: {`CREATE VIRTUAL TABLE session_search USING fts5(name)`, `CREATE TRIGGER session_search_insert AFTER INSERT ON sessions BEGIN INSERT INTO session_search(rowid,name) VALUES(new.rowid,new.name); END;`, `CREATE TRIGGER session_search_update AFTER UPDATE OF name ON sessions BEGIN UPDATE session_search SET name=new.name WHERE rowid=old.rowid; END;`, `CREATE TRIGGER session_search_delete AFTER DELETE ON sessions BEGIN DELETE FROM session_search WHERE rowid=old.rowid; END;`, `INSERT INTO session_search(rowid,name) SELECT rowid,name FROM sessions`},
	37: {`CREATE VIRTUAL TABLE post_search USING fts5(text, author)`, `CREATE TRIGGER post_search_insert AFTER INSERT ON events WHEN new.type='post.created' BEGIN INSERT INTO post_search(rowid,text,author) VALUES(new.seq,coalesce(json_extract(CAST(new.body AS TEXT),'$.text'),''),CASE new.from_kind WHEN 'session' THEN coalesce((SELECT name FROM sessions WHERE id=new.from_id),'') ELSE new.from_kind END); END;`, `CREATE TRIGGER post_search_delete AFTER DELETE ON events WHEN old.type='post.created' BEGIN DELETE FROM post_search WHERE rowid=old.seq; END;`, `INSERT INTO post_search(rowid,text,author) SELECT seq,coalesce(json_extract(CAST(body AS TEXT),'$.text'),''),CASE from_kind WHEN 'session' THEN coalesce((SELECT name FROM sessions WHERE id=from_id),'') ELSE from_kind END FROM events WHERE type='post.created'`},
	36: {`ALTER TABLE deliveries ADD COLUMN handoff_from TEXT`},
	34: {`CREATE TABLE room_shared (room TEXT NOT NULL, host TEXT NOT NULL REFERENCES hosts(name), bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL, PRIMARY KEY(room, host))`},
	35: {`ALTER TABLE archives ADD COLUMN retry_at INTEGER NOT NULL DEFAULT 0`},
	32: {`ALTER TABLE events ADD COLUMN client_id TEXT`, `CREATE UNIQUE INDEX events_client_id ON events(room, from_kind, from_id, client_id) WHERE client_id IS NOT NULL`},
	33: {`ALTER TABLE sessions ADD COLUMN close_reason TEXT NOT NULL DEFAULT ''`},
	30: {`CREATE TABLE host_rooms (host TEXT NOT NULL REFERENCES hosts(name), room TEXT NOT NULL REFERENCES rooms(id), PRIMARY KEY(host, room))`},
	31: {`ALTER TABLE boxes DROP COLUMN parent`},
	28: {`ALTER TABLE pr_owners ADD COLUMN branch TEXT NOT NULL DEFAULT ''`, `UPDATE pr_owners SET branch=COALESCE((SELECT 'v2/'||json_extract(CAST(body AS TEXT),'$.session') FROM events WHERE type='pr.opened' AND json_extract(CAST(body AS TEXT),'$.repo')=pr_owners.repo AND json_extract(CAST(body AS TEXT),'$.number')=pr_owners.number ORDER BY seq DESC LIMIT 1),'')`},
	29: {`CREATE TABLE devices (id TEXT PRIMARY KEY, name TEXT NOT NULL, token_sha256 BLOB NOT NULL UNIQUE, paired_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER)`, `CREATE TABLE device_scopes (device TEXT NOT NULL REFERENCES devices(id), scope TEXT NOT NULL, PRIMARY KEY(device, scope))`, `CREATE TABLE pairing_codes (id INTEGER PRIMARY KEY CHECK(id=1), code_hash BLOB NOT NULL, scopes BLOB NOT NULL, expires_at INTEGER NOT NULL, tries INTEGER NOT NULL)`},
	27: {`CREATE TABLE backup_copy_status (id INTEGER PRIMARY KEY CHECK(id=1), last_success INTEGER, failure TEXT NOT NULL)`},
	26: {`ALTER TABLE sessions ADD COLUMN model_retry_at INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE sessions ADD COLUMN model_retries INTEGER NOT NULL DEFAULT 0`},
	25: {`ALTER TABLE turns ADD COLUMN stop_requested_at INTEGER`},
	24: {`ALTER TABLE hosts ADD COLUMN engine_host INTEGER`},
	23: {
		`ALTER TABLE sessions ADD COLUMN shell_jobs INTEGER NOT NULL DEFAULT 0`,
		`UPDATE sessions SET shell_jobs=MAX(0,COALESCE((SELECT shell_jobs FROM turns WHERE session=sessions.id AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 1),0))`,
	},
	22: {`ALTER TABLE hosts ADD COLUMN maintained_at INTEGER NOT NULL DEFAULT 0`},
	19: {`CREATE TABLE rules (room TEXT NOT NULL REFERENCES rooms(id), number INTEGER NOT NULL, text TEXT NOT NULL, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, created_at INTEGER NOT NULL, removed_at INTEGER, PRIMARY KEY(room, number))`},
	20: {"CREATE TABLE uploads (sha256 TEXT NOT NULL, room TEXT NOT NULL, PRIMARY KEY(sha256, room))"},
	18: {"CREATE TABLE conversation_reads (room TEXT NOT NULL, conversation TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY(room, conversation))"},
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
	14: {`ALTER TABLE hostexec_runs ADD COLUMN lost_at INTEGER`, `CREATE INDEX hostexec_runs_pending ON hostexec_runs(ended_at) WHERE ended_at IS NULL`, `UPDATE hostexec_runs SET ended_at=NULL,lost_at=unixepoch()*1000 WHERE stopped='lost' AND started_at+timeout_ms+10000>unixepoch()*1000`},
	15: {
		"ALTER TABLE events ADD COLUMN home TEXT",
		`UPDATE events SET home=json_extract(CAST(body AS TEXT),'$.home.session') WHERE type='post.created' AND json_extract(CAST(body AS TEXT),'$.home.kind')='dm'`,
		`UPDATE events SET home=(SELECT json_extract(value,'$.reply.home.session') FROM json_each(CAST(events.body AS TEXT),'$.replies') WHERE json_extract(value,'$.reply.home.kind')='dm' LIMIT 1) WHERE type='turn.ended'`,
		"CREATE INDEX events_room_home ON events(room, home, seq) WHERE home IS NOT NULL",
	},
	// Only an open turn reads started_seq again, when it ends.
	16: {
		"ALTER TABLE turns ADD COLUMN started_seq INTEGER",
		`UPDATE turns SET started_seq=(SELECT seq FROM events WHERE type='turn.started' AND json_extract(CAST(body AS TEXT),'$.turn')=turns.id) WHERE ended_at IS NULL`,
	},
	17: {
		`UPDATE operations SET state='expired', decision=CAST(json_object('by',json_object('kind','engine'),'approved',json('false'),'reason','Host access requests changed; ask again','at',strftime('%Y-%m-%dT%H:%M:%SZ','now')) AS BLOB) WHERE kind='hostexec.grant' AND state='pending' AND json_type(CAST(args AS TEXT),'$.commands') IS NOT NULL`,
		`UPDATE grants SET revoked_at=unixepoch()*1000 WHERE revoked_at IS NULL AND expired_at IS NULL AND NOT (kind='hostexec' AND by_kind='engine' AND by_id='' AND to_kind='session' AND id='grant_'||to_id AND EXISTS (SELECT 1 FROM sessions WHERE id=grants.to_id AND room=grants.room AND parent IS NULL))`,
		`UPDATE hostexec_runs SET stopped='revoked' WHERE ended_at IS NULL AND stopped='' AND grant_id IN (SELECT id FROM grants WHERE revoked_at IS NOT NULL)`,
		`ALTER TABLE grants ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE grants DROP COLUMN mode`,
		`ALTER TABLE grants DROP COLUMN commands`,
	},
	21: {
		`ALTER TABLE boxes ADD COLUMN want TEXT NOT NULL DEFAULT 'run'`,
		`ALTER TABLE boxes ADD COLUMN due_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE boxes ADD COLUMN failures INTEGER NOT NULL DEFAULT 0`,
		`UPDATE boxes SET want='stop' WHERE session IN (SELECT id FROM sessions WHERE state='dormant')`,
		`UPDATE boxes SET want='close' WHERE session IN (SELECT id FROM sessions WHERE state='closing')`,
		`UPDATE sessions SET state='open' WHERE state IN ('dormant','pending')`,
	},
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
