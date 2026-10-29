package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotReadsCommittedStateDuringWrite(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	db, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dataDir) })
	if _, err := db.Exec("CREATE TABLE notes(body TEXT); INSERT INTO notes VALUES ('committed')"); err != nil {
		t.Fatal(err)
	}

	writer, err := sql.Open("sqlite", filepath.Join(dataDir, "dyna.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO notes VALUES ('uncommitted')"); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(dir, "snapshot.db")
	path, err := Snapshot(ctx, db, destination)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination {
		t.Fatalf("snapshot path = %q, want %q", path, destination)
	}
	if info, err := os.Stat(destination); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	saved, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	var integrity string
	if err := saved.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("snapshot integrity = %q, %v", integrity, err)
	}
	var bodies []string
	rows, err := saved.Query("SELECT body FROM notes ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0] != "committed" {
		t.Fatalf("snapshot rows = %v, want only the committed row", bodies)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRefusesExistingDestinations(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	db, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(dataDir) })
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(context.Background(), db, target); err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Errorf("Snapshot(existing file) error = %v, want destination already exists", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("destination symlink unavailable: %v", err)
	}
	if _, err := Snapshot(context.Background(), db, link); err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Errorf("Snapshot(destination symlink) error = %v, want destination already exists", err)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "keep" {
		t.Fatalf("existing destination = %q, %v; want unchanged content", body, err)
	}
}
