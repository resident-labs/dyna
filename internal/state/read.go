package state

import (
	"context"
	"database/sql"

	"dyna-agent/internal/database"
)

// Reader queries one database snapshot. It does not expose write methods.
type Reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Read runs fn on one snapshot on the shared handle's read side.
// fn must only read. Reads do not publish or count as writes in Health.
func (w *Writer) Read(ctx context.Context, fn func(Reader) error) error {
	return database.Read(ctx, w.db, func(tx *sql.Tx) error { return fn(tx) })
}
