package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreshSchema(t *testing.T) {
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareSchema(s.writer); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestUpgradeAddsArchives(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec("DROP TABLE archives; UPDATE meta SET value='3' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archives(context.Background(), "session"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedInitializationCanRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".store-init-interrupted"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestV1Guard(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dyna.db"), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir, time.Now)
	var e *Error
	if !errors.As(err, &e) || e.Code != StoreFromV1 {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store created: %v", err)
	}
}
func TestFloor(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = open(dir, time.Now, schemaVersion+1, "fake-commit", nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != StoreTooOld || !strings.Contains(e.Message, "fake-commit") {
		t.Fatalf("got %v", err)
	}
}
func TestWriteSurvivesClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec("INSERT INTO meta VALUES ('test','value')"); return err })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := s.Read().QueryRow("SELECT value FROM meta WHERE key='test'").Scan(&value); err != nil || value != "value" {
		t.Fatalf("got %q, %v", value, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestCancelledWriteDoesNotFailClose(t *testing.T) {
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = s.Write(ctx, func(*sql.Tx) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A commit that loses the race with the caller's cancel is the caller's error.
// The write function rolls back before it returns, so Commit sees a finished
// transaction without waiting on database/sql's asynchronous rollback.
func TestCommitLostToCancelIsNotWriterError(t *testing.T) {
	s, err := Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = s.Write(ctx, func(tx *sql.Tx) error {
		cancel()
		// database/sql may already have rolled back after seeing the cancel. Either way
		// the transaction is finished here, so Commit reaches ErrTxDone every time.
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return err
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write returned %v, want context.Canceled", err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO meta VALUES ('after-cancel','value')")
		return err
	})
	if err != nil {
		t.Fatalf("write after the cancelled commit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close reported the caller's cancel: %v", err)
	}
}

func TestCheckpointDoesNotBlockWrite(t *testing.T) {
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	s, err := open(t.TempDir(), time.Now, floorVersion, floorCommit, func() error { close(started); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- s.checkpointOnce() }()
	<-started
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO meta VALUES ('checkpoint-test','value')")
		return err
	})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
