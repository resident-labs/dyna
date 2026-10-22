package database

import "testing"

// Production stays on WAL with synchronous=NORMAL: commits do not flush the
// disk, a process crash loses nothing, and only an OS crash or power loss can
// drop the last commits.
func TestProductionUsesNormalSync(t *testing.T) {
	if got := syncPragma(false); got != "synchronous(NORMAL)" {
		t.Fatalf("production sync pragma = %q, want synchronous(NORMAL)", got)
	}
}

// Test binaries open throwaway databases without the disk flush.
func TestTestsOpenWithoutDiskFlush(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var mode int
	var journal string
	if err := db.QueryRow("PRAGMA synchronous").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if mode != 0 || journal != "wal" {
		t.Fatalf("synchronous = %d, journal = %q; want 0 (OFF) with wal", mode, journal)
	}
}
