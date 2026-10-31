package eval

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// Journal stores one evaluation's attempt claims in a private scratch database.
// A crash after a claim never permits an unrecorded repeat.
type Journal struct{ db *sql.DB }

// OpenJournal opens or creates a journal. The caller chooses a private scratch
// path and must never reuse the file for a different evaluation.
func OpenJournal(path string) (*Journal, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS attempts (
		task INTEGER NOT NULL, provider TEXT NOT NULL, arm TEXT NOT NULL,
		attempt INTEGER NOT NULL, finished INTEGER NOT NULL DEFAULT 0,
		retryable INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (task, provider, arm, attempt)
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Journal{db: db}, nil
}

// Begin durably claims the first attempt, or one retry after a recorded early
// provider failure. It refuses duplicate, missing and unfinished attempts.
func (j *Journal) Begin(ctx context.Context, c Cell, attempt int) error {
	if attempt < 1 || attempt > 2 {
		return fmt.Errorf("invalid attempt %d", attempt)
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if attempt == 2 {
		var finished, retryable bool
		if err := tx.QueryRowContext(ctx, `SELECT finished,retryable FROM attempts WHERE task=? AND provider=? AND arm=? AND attempt=1`, c.Task, c.Provider, c.Arm).Scan(&finished, &retryable); err != nil {
			return fmt.Errorf("first attempt unavailable: %w", err)
		}
		if !finished || !retryable {
			return fmt.Errorf("retry is not permitted")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO attempts (task,provider,arm,attempt) VALUES (?,?,?,?)`, c.Task, c.Provider, c.Arm, attempt); err != nil {
		return err
	}
	return tx.Commit()
}

// Finish records retry eligibility without storing prompts or model output.
func (j *Journal) Finish(ctx context.Context, c Cell, attempt int, a Attempt) error {
	retryable := RetryAllowed(attempt, a.ProviderFailure, a.OutputStarted)
	result, err := j.db.ExecContext(ctx, `UPDATE attempts SET finished=1,retryable=? WHERE task=? AND provider=? AND arm=? AND attempt=? AND finished=0`, retryable, c.Task, c.Provider, c.Arm, attempt)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("attempt is missing or already finished")
	}
	return nil
}

// Close closes the journal.
func (j *Journal) Close() error { return j.db.Close() }
