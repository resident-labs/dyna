package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Counter names a revision and holds its fixed SQL statements.
type Counter struct {
	bump, next, read string
}

// NewCounter constructs a counter with a fixed, trusted name.
func NewCounter(name string) Counter {
	if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789._") != "" {
		panic("invalid counter name")
	}
	bump := "INSERT INTO revisions(name,n) VALUES('" + name + "',1) ON CONFLICT(name) DO UPDATE SET n=n+1"
	return Counter{bump: bump, next: bump + " RETURNING n", read: "SELECT n FROM revisions WHERE name='" + name + "'"}
}

// Bump advances the counter in the caller's write transaction.
func (c Counter) Bump(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, c.bump)
	return err
}

// Next advances the counter and returns the new value.
func (c Counter) Next(ctx context.Context, conn *sql.Conn) (int64, error) {
	var n int64
	err := conn.QueryRowContext(ctx, c.next).Scan(&n)
	return n, err
}

// Read returns zero for a counter that has not been bumped.
// The variadic arguments match database/sql's QueryRowContext method.
func (c Counter) Read(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, c.read).Scan(&n)
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
