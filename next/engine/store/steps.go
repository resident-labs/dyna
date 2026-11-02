package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const schemaVersion = 76

// Every schema change adds an entry here and updates schema.sql. A store below floorVersion stops at floorCommit, so
// steps below the floor go when the floor moves. Later steps are ordinary.
var steps = map[int][]string{
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
