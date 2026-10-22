package notices

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestRowsSurviveMissingLegacyFile(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { _ = database.Close(s.Dir) })
	n, err := s.Post(Notice{Key: "disk", Text: "Saved in SQLite."})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Path()); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].ID != n.ID {
		t.Fatalf("rows without mirror = %+v, %v", list, err)
	}
}

func TestForeignFileImportsBeforeWriteAndDamageKeepsRows(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { _ = database.Close(s.Dir) })
	old := Notice{ID: "old", Key: "disk", Text: "Old binary."}
	if err := store.WriteJSON(s.Path(), []Notice{old}, 0600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Text != old.Text {
		t.Fatalf("import = %+v, %v", list, err)
	}
	if err := os.WriteFile(s.Path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil || len(list) != 1 || list[0].ID != old.ID {
		t.Fatalf("damaged legacy file lost rows = %+v, %v", list, err)
	}
	old.Text = "Changed by old binary."
	if err := store.WriteJSON(s.Path(), []Notice{old}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(Notice{Key: "provider", Text: "New binary."}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Text != old.Text {
		t.Fatalf("legacy mirror lost foreign update = %+v", list)
	}
}

func TestRevisionIncreasesAfterNewestIsDismissed(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { _ = database.Close(s.Dir) })
	previous, err := s.Revision()
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		next, err := s.Revision()
		if err != nil || next <= previous {
			t.Fatalf("revision %d after %d: %v", next, previous, err)
		}
		previous = next
	}
	if _, err := s.Post(Notice{Key: "older"}); err != nil {
		t.Fatal(err)
	}
	check()
	newest, err := s.Post(Notice{Key: "newest"})
	if err != nil {
		t.Fatal(err)
	}
	check()
	if err := s.Dismiss(newest.ID); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := s.Post(Notice{Key: "next"}); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestMigrationDoesNotReadLegacyFile(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { _ = database.Close(s.Dir) })
	if err := os.WriteFile(s.Path(), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.Dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var stamps int
	if err := db.QueryRow("SELECT count(*) FROM state_mirrors WHERE key=?", mirrorKey).Scan(&stamps); err != nil || stamps != 0 {
		t.Fatalf("startup imported file: stamps=%d, %v", stamps, err)
	}
}

func TestPostDoesNotRewriteUnchangedNotice(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { _ = database.Close(s.Dir) })
	if _, err := s.Post(Notice{Key: "older"}); err != nil {
		t.Fatal(err)
	}
	db, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	state.For(db).SetPublish(func(event hub.Event) { t.Errorf("unexpected bare event: %+v", event) })
	if _, err := db.Exec("CREATE TRIGGER keep_older BEFORE UPDATE ON notices WHEN OLD.key='older' BEGIN SELECT RAISE(ABORT,'rewrote unchanged notice'); END; CREATE TRIGGER retain_older BEFORE DELETE ON notices WHEN OLD.key='older' BEGIN SELECT RAISE(ABORT,'deleted unchanged notice'); END;"); err != nil {
		t.Fatal(err)
	}
	newest, err := s.Post(Notice{Key: "newest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss(newest.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNoticeLockHonorsCancellationAndRelease(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	unlock, err := s.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if unlock != nil {
			unlock()
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if release, err := s.lock(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("cancelled lock = %v", err)
	}
	unlock()
	unlock = nil
	release, err := s.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
