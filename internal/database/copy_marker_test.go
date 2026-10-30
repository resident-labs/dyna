package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyMarkers(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	db, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Close(source) })
	if err := ClearCopyMarker(source); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	snapshot, err := Snapshot(context.Background(), db, filepath.Join(dir, "snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	backupPath, err := backup(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now()
	for _, path := range []string{snapshot, backupPath} {
		copyDir := t.TempDir()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(copyDir, "dyna.db"), data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := CopyTime(copyDir)
		if err != nil || got.Before(start) || got.After(end) {
			t.Fatalf("copy time for %s = %v, %v", path, got, err)
		}
		if err := ClearCopyMarker(copyDir); err != nil {
			t.Fatal(err)
		}
		got, err = CopyTime(copyDir)
		if err != nil || !got.IsZero() {
			t.Fatalf("cleared copy time = %v, %v", got, err)
		}
		if err := Close(copyDir); err != nil {
			t.Fatal(err)
		}
	}
	got, err := CopyTime(source)
	if err != nil || !got.IsZero() {
		t.Fatalf("source copy time = %v, %v", got, err)
	}
}
