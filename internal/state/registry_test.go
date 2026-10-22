package state

import (
	"context"
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// Every store in a process writes through one Writer per database, so the
// engine's publish function and Health see all of its writes.
func TestForSharesOneWriterThatPublishesOnce(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := For(db)
	if For(db) != w {
		t.Fatal("For returned two Writers for one database")
	}
	var published []hub.Event
	w.SetPublish(func(c hub.Event) { published = append(published, c) })
	if err := For(db).Write(context.Background(), hub.Event{Topic: "things"}, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Topic != "things" {
		t.Fatalf("published %+v, want one things event", published)
	}
}

// Closing a handle drops its Writer, so the registry does not grow with
// handles that are gone.
func TestCloseDropsTheWriter(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	For(db)
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	writers.Lock()
	_, kept := writers.byDB[db]
	writers.Unlock()
	if kept {
		t.Fatal("the closed handle's Writer is still registered")
	}
}
