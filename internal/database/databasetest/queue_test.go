package databasetest_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/database/databasetest"
)

func TestWaitForWriterObservesQueue(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	holding, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- database.Immediate(t.Context(), db, func(*sql.Conn) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() {
		close(release)
		if err := <-held; err != nil {
			t.Error(err)
		}
	}()

	before := database.Waiting()
	ctx, cancel := context.WithCancel(t.Context())
	waiting := make(chan error, 1)
	go func() {
		waiting <- database.Immediate(ctx, db, func(*sql.Conn) error {
			return errors.New("the waiting write ran before the holder released")
		})
	}()
	defer func() {
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Errorf("cancel waiting write: %v", err)
		}
	}()

	databasetest.WaitForWriter(t, before, waiting)
	if got := database.Waiting(); got <= before {
		t.Fatalf("helper returned before queue registration: %d -> %d", before, got)
	}
}
