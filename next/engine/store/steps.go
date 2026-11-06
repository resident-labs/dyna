package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 97

// Every schema change adds an entry here and updates schema.sql. A store below floorVersion stops at floorCommit, so
// steps below the floor go when the floor moves. Later steps are ordinary.
var steps = map[int][]string{
	// A nonempty original host consumes one safe other-host create retry, including across restarts.
	96: {`ALTER TABLE boxes ADD COLUMN create_retry_host TEXT NOT NULL DEFAULT ''`},
	// shell.job.ended lost exitCode: End says how a job ended. Bodies decode strictly, and they stay BLOBs.
	73: {`UPDATE events SET body=CAST(json_remove(CAST(body AS TEXT),'$.exitCode') AS BLOB) WHERE type='shell.job.ended' AND json_type(CAST(body AS TEXT),'$.exitCode') IS NOT NULL`},
	// A resumed run owns the branches of the run it resumes, so a run keeps which run that is.
	74: {`ALTER TABLE workflow_runs ADD COLUMN resumes TEXT NOT NULL DEFAULT ''`},
	// A reload gives a running run a new script, so a run keeps each version and a step records the one that started it. A
	// step a resumed run kept started before the run did, in another run, and has none.
	75: {
		`ALTER TABLE workflow_runs ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE workflow_steps ADD COLUMN version INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE workflow_scripts (run TEXT NOT NULL REFERENCES workflow_runs(id), version INTEGER NOT NULL, script TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(run, version))`,
		`INSERT INTO workflow_scripts(run,version,script,created_at) SELECT id,1,script,started_at FROM workflow_runs`,
		`UPDATE workflow_steps SET version=1 WHERE started_at>=(SELECT started_at FROM workflow_runs WHERE id=workflow_steps.run)`,
	},
	// A script's status() lines stay on its run, and a step names the subject it is about.
	76: {`ALTER TABLE workflow_runs ADD COLUMN status_lines TEXT NOT NULL DEFAULT '[]'`, `ALTER TABLE workflow_steps ADD COLUMN lane TEXT NOT NULL DEFAULT ''`},
	// A post to a parent that can't receive goes up to the nearest ancestor that can, and keeps whom it was for and why. Close no
	// longer hands inputs over, so no row names a handoff.
	77: {`ALTER TABLE deliveries RENAME COLUMN handoff_from TO meant_for`, `ALTER TABLE deliveries ADD COLUMN meant_why TEXT NOT NULL DEFAULT ''`},
	// A host has two switches: agents for session boxes and ci for CI runners. A host that a repository already runs CI on
	// keeps it, and every host keeps its agents.
	78: {
		`ALTER TABLE hosts ADD COLUMN agents INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE hosts ADD COLUMN ci INTEGER NOT NULL DEFAULT 0`,
		`UPDATE hosts SET ci=1 WHERE name IN (SELECT host FROM repo_ci_hosts)`,
	},
	// A workflow's runs-on picks a runner's size, and admission picks its host, so a repository's CI is on or off, with an
	// optional bound. A repository that named hosts is on, bounded by the runners it allowed them. A pool's node serves the
	// runners of the rooms that name its pool, as it serves their boxes.
	79: {
		`ALTER TABLE repos ADD COLUMN ci INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE repos ADD COLUMN ci_max INTEGER NOT NULL DEFAULT 0`,
		`UPDATE repos SET ci=1, ci_max=(SELECT SUM(runners) FROM repo_ci_hosts WHERE repo=repos.repo) WHERE repo IN (SELECT repo FROM repo_ci_hosts)`,
		`DROP TABLE repo_ci_hosts`,
		`UPDATE hosts SET ci=1 WHERE pool!=''`,
	},
	// A room's root may decide its sessions' host exec grant requests when the owner allows it; off until then.
	83: {`ALTER TABLE rooms ADD COLUMN root_approves_hostexec INTEGER NOT NULL DEFAULT 0`},
	// Backups are gone, so nothing records an off-host copy.
	80: {`DROP TABLE backup_copy_status`},
	82: {`ALTER TABLE hosts ADD COLUMN disk_total INTEGER NOT NULL DEFAULT 0`},
	// A host added exec only takes host-exec runs and nothing else, whatever its switches say.
	84: {`ALTER TABLE hosts ADD COLUMN exec_only INTEGER NOT NULL DEFAULT 0`},
	// The owner can say which host a paired device is on, so its inputs say where they come from.
	85: {`ALTER TABLE devices ADD COLUMN host TEXT NOT NULL DEFAULT ''`},
	// The owner-class operations a room's root may also decide are one set of kinds, which replaces the host exec flag. DROP COLUMN needs SQLite 3.35 or later.
	86: {
		`ALTER TABLE rooms ADD COLUMN root_approves TEXT NOT NULL DEFAULT '[]'`,
		`UPDATE rooms SET root_approves='["hostexec"]' WHERE root_approves_hostexec=1`,
		`ALTER TABLE rooms DROP COLUMN root_approves_hostexec`,
	},
	// A running turn records when it last used a tool, so a long turn that keeps working is not a stalled one.
	87: {`ALTER TABLE turns ADD COLUMN active_at INTEGER NOT NULL DEFAULT 0`},
	// Close keeps no archive (spec 5.9). A close that was held after failed archives, or was retrying its archive, is now a plain
	// close: the box is removed with nothing saved, and its retry state is cleared.
	88: {
		`UPDATE boxes SET want='close',state='closing',reason='',failures=0,due_at=0 WHERE want='hold'`,
		`UPDATE boxes SET reason='',failures=0,due_at=0 WHERE want='close' AND state='closing'`,
		`DROP TABLE IF EXISTS archives`,
	},
	// A session's branch is recorded, not derived from its ID. A session that has a repository keeps the branch it had.
	89: {
		`CREATE TABLE session_branches (repo TEXT NOT NULL, branch TEXT NOT NULL, session TEXT NOT NULL REFERENCES sessions(id), created_at INTEGER NOT NULL, PRIMARY KEY(repo, branch, session))`,
		`CREATE INDEX session_branches_session ON session_branches(session)`,
		`INSERT INTO session_branches(repo,branch,session,created_at) SELECT repo,'v2/'||id,id,created_at FROM sessions WHERE repo!=''`,
	},
	// A host reports its GPUs, and a box keeps the VRAM it asked of a card and the card it was given.
	90: {
		`ALTER TABLE hosts ADD COLUMN gpus TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE boxes ADD COLUMN gpu_gib INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE boxes ADD COLUMN gpu_index INTEGER NOT NULL DEFAULT 0`,
	},
	// The owner's workflow settings: one row for the global scope (room '') and one for each room that overrides it.
	91: {`CREATE TABLE workflow_settings (room TEXT PRIMARY KEY, settings TEXT NOT NULL)`},
	// A room's githubWrites setting (spec 7.9), JSON; `{}` is off.
	92: {`ALTER TABLE rooms ADD COLUMN github_writes TEXT NOT NULL DEFAULT '{}'`},
	// The default-branch head whose CI git.main_ci_finished has settled (spec 7.7). A head a store already holds starts settled, so enabling the event reports nothing from before it.
	93: {`ALTER TABLE repos ADD COLUMN ci_settled TEXT NOT NULL DEFAULT ''`, `UPDATE repos SET ci_settled=main_head`},
	// The recent default-branch heads of each repository and whether git.main_ci_finished has settled each (spec 7.7), in place of repos.ci_settled. A head a store already holds starts settled, so nothing from before the list is reported.
	94: {
		`CREATE TABLE repo_heads (repo TEXT NOT NULL REFERENCES repos(repo), seq INTEGER PRIMARY KEY AUTOINCREMENT, sha TEXT NOT NULL, seen_at INTEGER NOT NULL, settled INTEGER NOT NULL DEFAULT 0, UNIQUE(repo, sha))`,
		`INSERT INTO repo_heads(repo,sha,seen_at,settled) SELECT repo,main_head,0,1 FROM repos WHERE main_head<>''`,
		`ALTER TABLE repos DROP COLUMN ci_settled`,
	},
	// Services (spec 5.18): a long-running container on a host, its write-only secrets, and what a host that was offline still holds of a removed one.
	95: {
		`CREATE TABLE services (id TEXT PRIMARY KEY, room TEXT NOT NULL REFERENCES rooms(id), name TEXT NOT NULL, spec BLOB NOT NULL, generation INTEGER NOT NULL, host TEXT REFERENCES hosts(name), cpu_milli INTEGER NOT NULL, mem_bytes INTEGER NOT NULL, disk_bytes INTEGER NOT NULL, image_bytes INTEGER NOT NULL DEFAULT 0, image_estimate INTEGER NOT NULL DEFAULT 0, want TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', cause TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', last_state TEXT NOT NULL DEFAULT '', deployed_generation INTEGER NOT NULL DEFAULT 0, image_ref TEXT NOT NULL DEFAULT '', previous_ref TEXT NOT NULL DEFAULT '', target_ref TEXT NOT NULL DEFAULT '', deployed_spec TEXT NOT NULL DEFAULT '', unit_sha TEXT NOT NULL DEFAULT '', restarts INTEGER NOT NULL DEFAULT 0, since INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL, placed_at INTEGER NOT NULL DEFAULT 0, rev INTEGER NOT NULL DEFAULT 0, by_kind TEXT NOT NULL, by_id TEXT NOT NULL)`,
		`CREATE UNIQUE INDEX services_room_name ON services(room, name) WHERE want != 'remove'`,
		`CREATE INDEX services_host ON services(host) WHERE host IS NOT NULL`,
		`CREATE TABLE service_secrets (service TEXT NOT NULL REFERENCES services(id), name TEXT NOT NULL, value BLOB NOT NULL, set_at INTEGER NOT NULL, PRIMARY KEY(service, name))`,
		`CREATE TABLE service_leftovers (host TEXT NOT NULL REFERENCES hosts(name), room TEXT NOT NULL, service TEXT NOT NULL, PRIMARY KEY(host, service))`,
		`CREATE TABLE service_image_sizes (room TEXT NOT NULL, source TEXT NOT NULL, bytes INTEGER NOT NULL, measured_at INTEGER NOT NULL, PRIMARY KEY(room, source))`,
	},
	81: {
		`ALTER TABLE hosts ADD COLUMN create_failures INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE hosts ADD COLUMN create_problem TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE hosts ADD COLUMN create_generation INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE boxes ADD COLUMN create_generation INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE boxes ADD COLUMN failed_create INTEGER NOT NULL DEFAULT 0`,
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
