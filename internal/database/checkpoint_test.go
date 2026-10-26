package database

import (
	"context"
	"testing"
)

// Commits must not pay for a checkpoint: with the default threshold of 1000
// pages, the COMMIT that crossed it copied the WAL while it held the writer lock.
func TestCommitDoesNotCheckpointUntilTheEngineDoes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer Close(dir)
	if _, err := db.Exec("CREATE TABLE blobs(data BLOB)"); err != nil {
		t.Fatal(err)
	}
	// 6 MB is about 1500 pages, past the old threshold and under the backstop.
	for range 6 {
		if _, err := db.Exec("INSERT INTO blobs VALUES (zeroblob(1048576))"); err != nil {
			t.Fatal(err)
		}
	}
	// A PASSIVE checkpoint after an automatic one finds nothing left to move
	// from the earlier frames. Here every frame is still waiting.
	got, err := Checkpoint(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if got.Busy || got.Frames < 1500 || got.Moved != got.Frames {
		t.Fatalf("Checkpoint = %+v, want all of at least 1500 frames moved", got)
	}
	var moved int
	if err := db.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&moved); err != nil || moved != 6 {
		t.Fatalf("rows after checkpoint = %d, %v", moved, err)
	}
}
