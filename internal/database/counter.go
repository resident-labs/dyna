package database

import (
	"context"
	"database/sql"
	"errors"
)

// Counter names one row of the revisions table.
type Counter string

// Bump advances the counter in the caller's write transaction.
func (c Counter) Bump(ctx context.Context, conn *sql.Conn) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, "INSERT INTO revisions(name,n) VALUES(?,1) ON CONFLICT(name) DO UPDATE SET n=n+1 RETURNING n", c).Scan(&n)
	return n, err
}

// Read returns zero for a counter that has not been bumped.
func (c Counter) Read(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT n FROM revisions WHERE name=?", c).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// CreateRevisions creates the shared counter table during a migration.
func CreateRevisions(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS revisions(name TEXT PRIMARY KEY, n INTEGER NOT NULL) WITHOUT ROWID")
	return err
}
