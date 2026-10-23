// Package runstoretest seeds canonical run records for dependent package tests.
package runstoretest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
)

// Write creates the retained run folder and writes the row at a new revision.
func Write(t testing.TB, dataDir string, m runstore.Meta) {
	t.Helper()
	if err := runstore.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "runs", m.ID), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		ctx := context.Background()
		var rev int64
		if err := conn.QueryRowContext(ctx, "UPDATE runs_rev SET n=n+1 RETURNING n").Scan(&rev); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO runs(id,session,started,body,rev) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET session=excluded.session,started=excluded.started,body=excluded.body,rev=excluded.rev", m.ID, m.Session, m.StartedAt.UnixNano(), string(raw), rev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
