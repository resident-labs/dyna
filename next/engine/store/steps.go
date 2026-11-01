package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 64

// Every schema change adds an entry here and updates schema.sql.
var steps = map[int][]string{
	63: {
		`CREATE TABLE subscriptions (id TEXT PRIMARY KEY, scope TEXT NOT NULL, room TEXT REFERENCES rooms(id), session TEXT REFERENCES sessions(id), every_ms INTEGER NOT NULL, wake_kind TEXT NOT NULL, wake_id TEXT NOT NULL, run_image TEXT NOT NULL, run_size TEXT NOT NULL, created_at INTEGER NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, due_at INTEGER NOT NULL DEFAULT 0, hold_until INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0, last_at INTEGER NOT NULL DEFAULT 0, last_result TEXT NOT NULL DEFAULT '', last_host TEXT NOT NULL DEFAULT '', last_seq INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX subscriptions_room ON subscriptions(room)`,
		`CREATE TABLE subscription_events (sub TEXT NOT NULL REFERENCES subscriptions(id), event_type TEXT NOT NULL, PRIMARY KEY(sub, event_type))`,
		`CREATE TABLE subscription_args (sub TEXT NOT NULL REFERENCES subscriptions(id), pos INTEGER NOT NULL, arg TEXT NOT NULL, PRIMARY KEY(sub, pos))`,
		`CREATE TABLE subscription_files (sub TEXT NOT NULL REFERENCES subscriptions(id), path TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(sub, path))`,
		`CREATE TABLE subscription_runs (id TEXT PRIMARY KEY, sub TEXT NOT NULL UNIQUE, room TEXT, host TEXT NOT NULL, size TEXT NOT NULL, started_at INTEGER NOT NULL)`,
		// Every existing root gets the subscription a new root gets. Dispatch starts from here, so old events fire nothing.
		`INSERT INTO subscriptions(id,scope,room,every_ms,wake_kind,wake_id,run_image,run_size,created_at) SELECT 'sub_'||r.id,'room',r.id,0,'session',s.id,'','',CAST(strftime('%s','now') AS INTEGER)*1000 FROM rooms r JOIN sessions s ON s.room=r.id AND s.parent IS NULL AND s.closed_at IS NULL WHERE r.state='open'`,
		`INSERT INTO subscription_events(sub,event_type) SELECT s.id,t.value FROM subscriptions s JOIN json_each('` + defaultEventsJSON + `') t`,
		`INSERT INTO meta(key,value) SELECT 'subscription_seq',COALESCE(MAX(seq),0) FROM events`,
	},
	62: {
		`CREATE TABLE goals (room TEXT NOT NULL REFERENCES rooms(id), number INTEGER NOT NULL, session TEXT REFERENCES sessions(id), text TEXT NOT NULL, done_when TEXT NOT NULL, state TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(room, number))`,
		`CREATE INDEX goals_holder ON goals(session) WHERE state='active'`,
		`CREATE TABLE goal_items (room TEXT NOT NULL, goal INTEGER NOT NULL, number INTEGER NOT NULL, text TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(room, goal, number), FOREIGN KEY(room, goal) REFERENCES goals(room, number))`,
		`CREATE TABLE goal_journal (room TEXT NOT NULL, goal INTEGER NOT NULL, number INTEGER NOT NULL, at INTEGER NOT NULL, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, text TEXT NOT NULL, PRIMARY KEY(room, goal, number), FOREIGN KEY(room, goal) REFERENCES goals(room, number))`,
		`CREATE TABLE checkins (session TEXT PRIMARY KEY REFERENCES sessions(id), reported_at INTEGER NOT NULL DEFAULT 0, report_seq INTEGER NOT NULL DEFAULT 0, silent_turn TEXT NOT NULL DEFAULT '', progress INTEGER NOT NULL DEFAULT 0, told INTEGER NOT NULL DEFAULT 0, told_at INTEGER NOT NULL DEFAULT 0, failure TEXT NOT NULL DEFAULT '')`,
		// Each open session's start goal becomes a real goal. Root holds no goals.
		`INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) SELECT room,ROW_NUMBER() OVER (PARTITION BY room ORDER BY created_at,id),id,goal,'','active',created_at,created_at FROM sessions WHERE goal!='' AND parent IS NOT NULL AND closed_at IS NULL`,
	},
	61: {
		`CREATE TABLE questions (id TEXT PRIMARY KEY, room TEXT NOT NULL REFERENCES rooms(id), session TEXT NOT NULL REFERENCES sessions(id), text TEXT NOT NULL, state TEXT NOT NULL, answer TEXT NOT NULL DEFAULT '', asked_at INTEGER NOT NULL, answered_at INTEGER)`,
		`CREATE INDEX questions_room_state ON questions(room, state, asked_at)`,
		`CREATE INDEX questions_session_pending ON questions(session, asked_at) WHERE state='pending'`,
		// Rooms made before this step gave their roots only the host grant.
		`INSERT INTO grants(id,kind,room,to_kind,to_id,hosts,by_kind,by_id,created_at) SELECT 'grant_ask_'||id,'ask-owner',room,'session',id,'[]','engine','',created_at FROM sessions WHERE parent IS NULL AND closed_at IS NULL`,
	},
	60: {`ALTER TABLE sessions ADD COLUMN compose TEXT NOT NULL DEFAULT ''`},
	59: {`CREATE TABLE session_accounts (session TEXT PRIMARY KEY REFERENCES sessions(id), account TEXT NOT NULL)`},
	58: {`ALTER TABLE turns ADD COLUMN dispatched_model TEXT NOT NULL DEFAULT ''`},
	57: {
		`ALTER TABLE rooms ADD COLUMN state TEXT NOT NULL DEFAULT 'open'`,
		`ALTER TABLE rooms ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
		`DROP TABLE uploads`,
		`CREATE TABLE room_leftovers (host TEXT NOT NULL REFERENCES hosts(name), room TEXT NOT NULL, box TEXT NOT NULL, slice TEXT NOT NULL, PRIMARY KEY(host, room, box))`,
		// An archive ref outlives its room until the expiry sweep deletes it, so the row no longer needs its session or repository.
		`CREATE TEMP TABLE archives_backup AS SELECT * FROM archives`,
		`DROP TABLE archives`,
		`CREATE TABLE archives (session TEXT NOT NULL, repo TEXT NOT NULL, ref TEXT NOT NULL, commit_sha TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, retry_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(session, repo))`,
		`INSERT INTO archives SELECT * FROM archives_backup`,
		`DROP TABLE archives_backup`,
	},
	56: {`CREATE TABLE repo_ci_hosts (repo TEXT NOT NULL REFERENCES repos(repo), host TEXT NOT NULL REFERENCES hosts(name), runners INTEGER NOT NULL, size TEXT NOT NULL, PRIMARY KEY(repo, host))`},
	55: {`ALTER TABLE hosts ADD COLUMN disk_floor INTEGER NOT NULL DEFAULT 21474836480`, `ALTER TABLE hosts ADD COLUMN disk_low_since INTEGER NOT NULL DEFAULT 0`, `CREATE TABLE disk_warnings (host TEXT PRIMARY KEY, at INTEGER NOT NULL)`},
	54: {`UPDATE grants SET expired_at=expires_at WHERE kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND expires_at IS NOT NULL AND expires_at<=CAST(strftime('%s','now') AS INTEGER)*1000`, `UPDATE grants SET expires_at=NULL WHERE kind='hostexec' AND revoked_at IS NULL AND expired_at IS NULL AND (expires_at IS NULL OR expires_at>CAST(strftime('%s','now') AS INTEGER)*1000)`, `UPDATE operations SET state='expired' WHERE kind='hostexec.grant' AND state='pending'`, `UPDATE operations SET args=json_remove(args,'$.expires','$.never') WHERE kind='hostexec.grant'`},
	53: {`CREATE TABLE search_keys (provider TEXT PRIMARY KEY, key TEXT NOT NULL)`},
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
