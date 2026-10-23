package hooks

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestRuntimeSurvivesRemovalOfLegacyFile(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if err := s.runtime("repo", func(st *state) error {
		st.Main = "head"
		st.Due["scheduled"] = now
		st.Notices["failed"] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.dir("repo"), "state.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "head" || !st.Due["scheduled"].Equal(now) || !st.Notices["failed"] {
			t.Fatalf("runtime lost with its legacy copy: %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeFailedUpdateKeepsPreviousRecord(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	if err := s.runtime("repo", func(st *state) error { st.Main = "old"; return nil }); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	if err := s.runtime("repo", func(st *state) error { st.Main = "new"; return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "old" {
			t.Fatalf("failed update persisted: %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRuntimeClaimsOnce(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	var wg sync.WaitGroup
	claims := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.runtime("repo", func(st *state) error {
				if !st.Notices["claim"] {
					st.Notices["claim"] = true
					claims++
				}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("claims = %d", claims)
	}

}

func TestRuntimeImportsLegacyAtEngineStartOnce(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	path := filepath.Join(s.dir("repo"), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, state{Main: "legacy"}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := s.runtimeDB()
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	key := filepath.Base(s.dir("repo"))
	if err := db.QueryRow("SELECT body FROM hook_runtime WHERE repo=?", key).Scan(&raw); err != nil || !strings.Contains(string(raw), "legacy") {
		t.Fatalf("imported runtime = %s, %v", raw, err)
	}
	if _, err := db.Exec("DELETE FROM hook_runtime"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hook_runtime").Scan(&count); err != nil || count != 0 {
		t.Fatalf("repeated startup import = %d rows, %v", count, err)
	}
}

func TestRuntimeCrashBeforeMirrorKeepsCommittedRow(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	if err := s.runtime("repo", func(st *state) error { st.Main = "old"; return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := s.runtimeDB()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a committed write whose process died before replacing its copy.
	if _, err := db.Exec("UPDATE hook_runtime SET body=?", []byte(`{"main":"committed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "committed" {
			t.Fatalf("stale copy replaced committed row: %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeWritePublishesNoUnusedEvent(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	db, err := s.runtimeDB()
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	statewrite.For(db).SetPublish(func(hub.Event) { published++ })
	if err := s.runtime("repo", func(st *state) error { st.Main = "head"; return nil }); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("runtime emitted %d unused events", published)
	}
}

func TestRuntimeWithoutSchemaOwnerDoesNotCreateTable(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.DataDir) })
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		}
	})
	if _, err := s.runtimeDB(); err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("non-engine schema open: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='hook_runtime'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("non-engine created %d tables, %v", count, err)
	}
}

// The stamps that #1480 kept in the mirror column move to state_mirrors, so
// a stale copy that its old stamp still covers is not imported over the row.
func TestRuntimeStampsMoveFromTheMirrorColumn(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "hooks.runtime", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), `CREATE TABLE hook_runtime(repo TEXT PRIMARY KEY, body BLOB NOT NULL, mirror TEXT NOT NULL DEFAULT '');`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir("repo"), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, state{Main: "stale-copy"}, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO hook_runtime(repo, body, mirror) VALUES (?, ?, ?)", filepath.Base(s.dir("repo")), []byte(`{"main":"row"}`), store.VersionOf(info).Key()); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "row" {
			t.Fatalf("the stale copy was imported over the row: %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A damaged stale file cannot block canonical runtime updates.
func TestRuntimeDamagedFileKeepsTheRow(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	if err := s.runtime("repo", func(st *state) error { st.Main = "row"; return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir("repo"), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "row" {
			t.Fatalf("the damaged file replaced the row: %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatalf("a damaged file blocked the update: %v", err)
	}
}
