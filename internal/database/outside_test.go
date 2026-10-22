package database_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

// Three call sites that write dyna.db.
func writeFromA(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := database.Immediate(context.Background(), db, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func writeFromB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := database.Immediate(context.Background(), db, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func writeFromC(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := database.Immediate(context.Background(), db, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func reportLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, database.OutsideWritesFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Outside the engine, each site that writes dyna.db is noted once, a site
// another process noted is not noted again, and the engine notes nothing
// (item 41, report only).
func TestWritesOutsideTheEngineAreNotedOncePerSite(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	writeFromA(t, db)
	if lines := reportLines(t, dir); len(lines) != 0 {
		t.Fatalf("the engine's write was noted: %v", lines)
	}

	// Another process noted writeFromC already.
	if err := os.WriteFile(filepath.Join(dir, database.OutsideWritesFile), []byte(`{"site":"dyna-agent/internal/database_test.writeFromC"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	writeFromA(t, db)
	writeFromA(t, db)
	writeFromB(t, db)
	writeFromC(t, db)
	lines := reportLines(t, dir)
	if len(lines) != 3 || !strings.Contains(lines[1], "writeFromA") || !strings.Contains(lines[2], "writeFromB") {
		t.Fatalf("report = %v; want writeFromC, then writeFromA and writeFromB once each", lines)
	}
}

// The report stops growing at its cap.
func TestOutsideWritesReportStopsAtItsCap(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	full := bytes.Repeat([]byte("x"), 1<<20)
	path := filepath.Join(dir, database.OutsideWritesFile)
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	writeFromB(t, db)
	if info, err := os.Stat(path); err != nil || info.Size() != int64(len(full)) {
		t.Fatalf("report after its cap: %v, %v; want unchanged", info.Size(), err)
	}
}

// The note is taken before the writer queue, so a writer that waits in the
// queue, or gives up there, is still noted, and the note's file read is not
// held inside the queue.
func TestOutsideWriteIsNotedBeforeTheWriterQueue(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := database.Immediate(ctx, db, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("a write with a canceled context succeeded")
	}
	if lines := reportLines(t, dir); len(lines) != 1 || !strings.Contains(lines[0], "TestOutsideWriteIsNotedBeforeTheWriterQueue") {
		t.Fatalf("report = %v; want the caller noted before the queue", lines)
	}
}
