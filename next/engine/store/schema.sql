CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE connect_connector (id INTEGER PRIMARY KEY CHECK(id=1), missing INTEGER NOT NULL CHECK(missing IN (0,1)));
CREATE TABLE connect_attempt (id INTEGER PRIMARY KEY CHECK(id=1), state TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE connect_link_state (id INTEGER PRIMARY KEY CHECK(id=1), epoch INTEGER NOT NULL CHECK(epoch>=0), linked INTEGER NOT NULL CHECK(linked IN (0,1)), generation INTEGER NOT NULL DEFAULT 0);
CREATE TABLE connect_nonces (nonce TEXT PRIMARY KEY, expires_at INTEGER NOT NULL);
CREATE TABLE rooms (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, cpus INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, paused_at INTEGER, state TEXT NOT NULL DEFAULT 'open', reason TEXT NOT NULL DEFAULT '');
CREATE TABLE repos (repo TEXT PRIMARY KEY, default_room TEXT NOT NULL REFERENCES rooms(id));
CREATE TABLE room_repos (room TEXT NOT NULL REFERENCES rooms(id), repo TEXT NOT NULL REFERENCES repos(repo), is_default INTEGER NOT NULL, PRIMARY KEY(room, repo));
CREATE UNIQUE INDEX room_repos_default ON room_repos(room) WHERE is_default = 1;
CREATE TABLE sessions (id TEXT PRIMARY KEY, room TEXT NOT NULL REFERENCES rooms(id), name TEXT NOT NULL, parent TEXT REFERENCES sessions(id), profile TEXT NOT NULL, state TEXT NOT NULL, goal TEXT NOT NULL, created_at INTEGER NOT NULL, closed_at INTEGER, repo TEXT NOT NULL DEFAULT '', shell_jobs INTEGER NOT NULL DEFAULT 0, model_retry_at INTEGER NOT NULL DEFAULT 0, model_retries INTEGER NOT NULL DEFAULT 0, close_reason TEXT NOT NULL DEFAULT '', compose TEXT NOT NULL DEFAULT '');
CREATE VIRTUAL TABLE session_search USING fts5(name);
CREATE TRIGGER session_search_insert AFTER INSERT ON sessions BEGIN INSERT INTO session_search(rowid,name) VALUES(new.rowid,new.name); END;
CREATE TRIGGER session_search_update AFTER UPDATE OF name ON sessions BEGIN UPDATE session_search SET name=new.name WHERE rowid=old.rowid; END;
CREATE TRIGGER session_search_delete AFTER DELETE ON sessions BEGIN DELETE FROM session_search WHERE rowid=old.rowid; END;
CREATE UNIQUE INDEX sessions_open_name ON sessions(room, name) WHERE closed_at IS NULL;
CREATE TABLE events (seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, at INTEGER NOT NULL, room TEXT REFERENCES rooms(id), type TEXT NOT NULL, from_kind TEXT NOT NULL, from_id TEXT NOT NULL, body BLOB NOT NULL, home TEXT, client_id TEXT);
CREATE VIRTUAL TABLE post_search USING fts5(text, author);
CREATE TRIGGER post_search_insert AFTER INSERT ON events WHEN new.type='post.created' BEGIN INSERT INTO post_search(rowid,text,author) VALUES(new.seq,coalesce(json_extract(CAST(new.body AS TEXT),'$.text'),''),CASE new.from_kind WHEN 'session' THEN coalesce((SELECT name FROM sessions WHERE id=new.from_id),'') ELSE new.from_kind END); END;
CREATE TRIGGER post_search_delete AFTER DELETE ON events WHEN old.type='post.created' BEGIN DELETE FROM post_search WHERE rowid=old.seq; END;
CREATE INDEX events_room_seq ON events(room, seq);
CREATE INDEX events_sender_type ON events(room, from_kind, from_id, type, seq);
CREATE UNIQUE INDEX events_client_id ON events(room, from_kind, from_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX events_room_home ON events(room, home, seq) WHERE home IS NOT NULL;
CREATE INDEX events_type_seq ON events(type, seq);
CREATE TABLE deliveries (to_kind TEXT NOT NULL, to_id TEXT NOT NULL, seq INTEGER NOT NULL REFERENCES events(seq), surface TEXT NOT NULL, reply_kind TEXT NOT NULL, reply_target_kind TEXT NOT NULL, reply_target_id TEXT NOT NULL, reply_home TEXT NOT NULL, reply_to TEXT NOT NULL, wake INTEGER NOT NULL, state TEXT NOT NULL, turn TEXT, queue TEXT, handoff_from TEXT, PRIMARY KEY(to_kind, to_id, seq));
CREATE TABLE turns (id TEXT PRIMARY KEY, session TEXT NOT NULL REFERENCES sessions(id), started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT NOT NULL, model TEXT NOT NULL, input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cache_read_tokens INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL, shell_jobs INTEGER NOT NULL DEFAULT -1, started_seq INTEGER, stop_requested_at INTEGER, sections BLOB, dispatched_model TEXT NOT NULL DEFAULT '');
CREATE TABLE hosts (name TEXT PRIMARY KEY, ssh TEXT NOT NULL, state TEXT NOT NULL, cpus INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, disk_bytes INTEGER NOT NULL, checked_at INTEGER NOT NULL, maintained_at INTEGER NOT NULL DEFAULT 0, engine_host INTEGER, disk_floor INTEGER NOT NULL DEFAULT 21474836480, disk_low_since INTEGER NOT NULL DEFAULT 0);
CREATE TABLE disk_warnings (host TEXT PRIMARY KEY, at INTEGER NOT NULL);
CREATE TABLE room_shared (room TEXT NOT NULL, host TEXT NOT NULL REFERENCES hosts(name), bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL, PRIMARY KEY(room, host));
CREATE TABLE host_rooms (host TEXT NOT NULL REFERENCES hosts(name), room TEXT NOT NULL REFERENCES rooms(id), PRIMARY KEY(host, room));
CREATE TABLE host_labels (host TEXT NOT NULL REFERENCES hosts(name), label TEXT NOT NULL, PRIMARY KEY(host, label));
CREATE TABLE repo_ci_hosts (repo TEXT NOT NULL REFERENCES repos(repo), host TEXT NOT NULL REFERENCES hosts(name), runners INTEGER NOT NULL, size TEXT NOT NULL, PRIMARY KEY(repo, host));
CREATE TABLE boxes (id TEXT PRIMARY KEY, session TEXT NOT NULL UNIQUE REFERENCES sessions(id), host TEXT REFERENCES hosts(name), image TEXT NOT NULL, size TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL, slice TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, want TEXT NOT NULL DEFAULT 'run', due_at INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0);
CREATE INDEX boxes_host_state ON boxes(host, state);
CREATE TABLE operations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT REFERENCES rooms(id), by_kind TEXT NOT NULL, by_id TEXT NOT NULL, class TEXT NOT NULL, state TEXT NOT NULL, card BLOB NOT NULL, args BLOB NOT NULL, args_sha256 BLOB NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, decision BLOB, result BLOB);
CREATE UNIQUE INDEX operations_pending_dedup ON operations(room, by_kind, by_id, kind, args_sha256) WHERE state = 'pending';
CREATE TABLE archives (session TEXT NOT NULL, repo TEXT NOT NULL, ref TEXT NOT NULL, commit_sha TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, retry_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(session, repo));
CREATE TABLE transcript_offsets (session TEXT NOT NULL REFERENCES sessions(id), path TEXT NOT NULL, acked INTEGER NOT NULL, PRIMARY KEY(session, path));
CREATE TABLE transcript_turns (session TEXT NOT NULL REFERENCES sessions(id), turn TEXT NOT NULL, path TEXT NOT NULL, start INTEGER NOT NULL, PRIMARY KEY(session, turn, path));
CREATE UNIQUE INDEX transcript_turns_path_start ON transcript_turns(session, path, start);
CREATE TABLE shell_jobs (session TEXT NOT NULL REFERENCES sessions(id), job TEXT NOT NULL, PRIMARY KEY(session, job));
CREATE TABLE event_files (seq INTEGER NOT NULL REFERENCES events(seq), sha256 TEXT NOT NULL, media TEXT NOT NULL, PRIMARY KEY(sha256, seq));
CREATE TABLE visuals (id TEXT PRIMARY KEY, room TEXT NOT NULL, author_kind TEXT NOT NULL, author_id TEXT NOT NULL, version INTEGER NOT NULL);
CREATE TABLE room_leftovers (host TEXT NOT NULL REFERENCES hosts(name), room TEXT NOT NULL, box TEXT NOT NULL, slice TEXT NOT NULL, PRIMARY KEY(host, room, box));
CREATE TABLE pr_owners (repo TEXT NOT NULL, number INTEGER NOT NULL, session TEXT NOT NULL REFERENCES sessions(id), goal TEXT NOT NULL, approved_head TEXT NOT NULL, branch TEXT NOT NULL DEFAULT '', head_repo TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '', draft INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'open', head TEXT NOT NULL DEFAULT '', review TEXT NOT NULL DEFAULT '', ci TEXT NOT NULL DEFAULT '', merge TEXT NOT NULL DEFAULT '', ci_told TEXT NOT NULL DEFAULT '', seen_reviews INTEGER NOT NULL DEFAULT 0, seen_review_comments INTEGER NOT NULL DEFAULT 0, seen_comments INTEGER NOT NULL DEFAULT 0, own_wake_at INTEGER NOT NULL DEFAULT 0, baseline INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(repo, number));

CREATE INDEX turns_session_ended ON turns(session, ended_at);
CREATE INDEX turns_session_started ON turns(session, started_seq);
CREATE INDEX turns_session_started_at ON turns(session, started_at DESC);
CREATE INDEX sessions_room_closed ON sessions(room, closed_at);
CREATE INDEX sessions_parent ON sessions(parent);
CREATE INDEX pr_owners_session ON pr_owners(session);
CREATE INDEX pr_owners_state ON pr_owners(state);
CREATE INDEX operations_state_expires ON operations(state, expires_at);
CREATE INDEX operations_pending_caller ON operations(by_kind, by_id, created_at, id) WHERE state='pending';

CREATE TABLE grants (id TEXT PRIMARY KEY, kind TEXT NOT NULL, room TEXT NOT NULL REFERENCES rooms(id), to_kind TEXT NOT NULL, to_id TEXT NOT NULL, hosts BLOB NOT NULL, expires_at INTEGER, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER, expired_at INTEGER, reason TEXT NOT NULL DEFAULT '');
CREATE INDEX grants_expiry ON grants(expires_at) WHERE expired_at IS NULL AND revoked_at IS NULL;
CREATE INDEX grants_room_principal ON grants(room, to_id);
CREATE TABLE hostexec_runs (op TEXT PRIMARY KEY REFERENCES operations(id), grant_id TEXT NOT NULL REFERENCES grants(id), host TEXT NOT NULL, scope TEXT NOT NULL, started_at INTEGER NOT NULL, timeout_ms INTEGER NOT NULL, ended_at INTEGER, output_bytes INTEGER NOT NULL DEFAULT 0, stopped TEXT NOT NULL DEFAULT '', lost_at INTEGER);
CREATE INDEX hostexec_runs_grant ON hostexec_runs(grant_id, ended_at);
CREATE INDEX hostexec_runs_pending ON hostexec_runs(ended_at) WHERE ended_at IS NULL;

CREATE TABLE conversation_reads (room TEXT NOT NULL, conversation TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY(room, conversation));

CREATE TABLE backup_copy_status (id INTEGER PRIMARY KEY CHECK(id=1), last_success INTEGER, failure TEXT NOT NULL);

CREATE TABLE devices (id TEXT PRIMARY KEY, name TEXT NOT NULL, token_sha256 BLOB NOT NULL UNIQUE, paired_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE TABLE device_scopes (device TEXT NOT NULL REFERENCES devices(id), scope TEXT NOT NULL, PRIMARY KEY(device, scope));
CREATE TABLE connect_revocations (relay_device TEXT PRIMARY KEY, revoked_at INTEGER NOT NULL);
CREATE TABLE connect_devices (relay_device TEXT PRIMARY KEY, device TEXT NOT NULL UNIQUE REFERENCES devices(id));
CREATE TABLE connect_sessions (id TEXT PRIMARY KEY, relay_device TEXT NOT NULL REFERENCES connect_devices(relay_device), origin TEXT NOT NULL, jkt TEXT NOT NULL, user_id TEXT NOT NULL, epoch INTEGER NOT NULL, generation INTEGER NOT NULL, token_hash BLOB NOT NULL UNIQUE, old1 BLOB, old2 BLOB, old3 BLOB, prev_until INTEGER NOT NULL, created_at INTEGER NOT NULL, access_until INTEGER NOT NULL, grant_end INTEGER NOT NULL, grant_at INTEGER NOT NULL);
CREATE TABLE pairing_codes (id INTEGER PRIMARY KEY CHECK(id=1), code_hash BLOB NOT NULL, scopes BLOB NOT NULL, expires_at INTEGER NOT NULL, tries INTEGER NOT NULL);

CREATE TABLE proxy_auths (id TEXT PRIMARY KEY, provider TEXT NOT NULL, record BLOB NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE search_keys (provider TEXT PRIMARY KEY, key TEXT NOT NULL);
CREATE TABLE accounts (id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL UNIQUE, provider TEXT NOT NULL, label TEXT NOT NULL);
CREATE TABLE account_rooms (account TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, room TEXT NOT NULL REFERENCES rooms(id), PRIMARY KEY(account, room));
CREATE TABLE account_alerts (account TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, window TEXT NOT NULL, kind TEXT NOT NULL, active INTEGER NOT NULL, PRIMARY KEY(account, window, kind));

CREATE TABLE session_accounts (session TEXT PRIMARY KEY REFERENCES sessions(id), account TEXT NOT NULL);

CREATE TABLE client_replays (method TEXT NOT NULL, caller_kind TEXT NOT NULL, caller_id TEXT NOT NULL, client_id TEXT NOT NULL, ref TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(method, caller_kind, caller_id, client_id));

CREATE TABLE questions (id TEXT PRIMARY KEY, room TEXT NOT NULL REFERENCES rooms(id), session TEXT NOT NULL REFERENCES sessions(id), text TEXT NOT NULL, state TEXT NOT NULL, answer TEXT NOT NULL DEFAULT '', asked_at INTEGER NOT NULL, answered_at INTEGER);
CREATE INDEX questions_room_state ON questions(room, state, asked_at);
CREATE INDEX questions_session_pending ON questions(session, asked_at) WHERE state='pending';
CREATE TABLE goals (room TEXT NOT NULL REFERENCES rooms(id), number INTEGER NOT NULL, session TEXT REFERENCES sessions(id), text TEXT NOT NULL, done_when TEXT NOT NULL, state TEXT NOT NULL, summary TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(room, number));
CREATE INDEX goals_holder ON goals(session) WHERE state='active';
CREATE TABLE goal_items (room TEXT NOT NULL, goal INTEGER NOT NULL, number INTEGER NOT NULL, text TEXT NOT NULL, state TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(room, goal, number), FOREIGN KEY(room, goal) REFERENCES goals(room, number));
CREATE TABLE goal_journal (room TEXT NOT NULL, goal INTEGER NOT NULL, number INTEGER NOT NULL, at INTEGER NOT NULL, by_kind TEXT NOT NULL, by_id TEXT NOT NULL, text TEXT NOT NULL, PRIMARY KEY(room, goal, number), FOREIGN KEY(room, goal) REFERENCES goals(room, number));
CREATE TABLE checkins (session TEXT PRIMARY KEY REFERENCES sessions(id), reported_at INTEGER NOT NULL DEFAULT 0, report_seq INTEGER NOT NULL DEFAULT 0, silent_turn TEXT NOT NULL DEFAULT '', progress INTEGER NOT NULL DEFAULT 0, told INTEGER NOT NULL DEFAULT 0, told_at INTEGER NOT NULL DEFAULT 0, failure TEXT NOT NULL DEFAULT '');
