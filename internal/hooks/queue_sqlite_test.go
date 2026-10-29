package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

// writeLegacySpool writes one previous-release queue file.
func writeLegacySpool(t *testing.T, dir, name string, e Event) {
	t.Helper()
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
		t.Fatal(err)
	}
}

// A previous release left pending events as files under hook-spool. The
// queue's first migration imports them in sequence order, and only then
// removes the files; a later enqueue lands after every imported row.
func TestImportMovesLegacySpoolIntoTheQueueInOrder(t *testing.T) {
	t.Run("order and files", func(t *testing.T) {
		s, repo := setup(t)
		t.Cleanup(func() { database.Close(s.DataDir) })
		spool := filepath.Join(s.DataDir, "hook-spool")
		if err := os.MkdirAll(spool, 0700); err != nil {
			t.Fatal(err)
		}
		// Listed in reverse order, and beside files the import must leave.
		writeLegacySpool(t, spool, "00000000000000000007.json", Event{Repo: repo, Event: AfterLand, Branch: "second"})
		writeLegacySpool(t, spool, "00000000000000000003.json", Event{Repo: repo, Event: AfterLand, Branch: "first"})
		if err := os.WriteFile(filepath.Join(spool, "notes.txt"), []byte("not an event"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.queueDB(); err != nil {
			t.Fatal(err)
		}
		events, err := s.QueuedEvents(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[0].Branch != "first" || events[1].Branch != "second" {
			t.Fatalf("imported events: %+v", events)
		}
		for _, name := range []string{"00000000000000000007.json", "00000000000000000003.json"} {
			if _, err := os.Stat(filepath.Join(spool, name)); !os.IsNotExist(err) {
				t.Fatalf("%s remains in the legacy spool: %v", name, err)
			}
		}
		if data, err := os.ReadFile(filepath.Join(spool, "notes.txt")); err != nil || string(data) != "not an event" {
			t.Fatalf("unrelated file changed: %q, %v", data, err)
		}
		if err := s.Enqueue(Event{Repo: repo, Event: AfterLand, Branch: "third"}); err != nil {
			t.Fatal(err)
		}
		if events, err = s.QueuedEvents(context.Background()); err != nil || len(events) != 3 || events[2].Branch != "third" {
			t.Fatalf("queue after enqueue: %+v, %v", events, err)
		}
	})
	t.Run("emptied directory removed", func(t *testing.T) {
		s, repo := setup(t)
		t.Cleanup(func() { database.Close(s.DataDir) })
		spool := filepath.Join(s.DataDir, "hook-spool")
		if err := os.MkdirAll(spool, 0700); err != nil {
			t.Fatal(err)
		}
		writeLegacySpool(t, spool, "00000000000000000001.json", Event{Repo: repo, Event: AfterLand})
		if _, err := s.queueDB(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(spool); !os.IsNotExist(err) {
			t.Fatalf("emptied legacy spool directory remains: %v", err)
		}
	})
}

// A retained snapshot event refuses the import — and so engine start —
// until the approved old-emitter drain clears it, as the old startup
// validator did. The refused source stays on disk and no row is written.
func TestImportRefusesRetainedSnapshot(t *testing.T) {
	for name, source := range map[string]string{
		"marker":      `{"event":"before-land","snapshot":true}`,
		"last of two": `{"snapshot":false,"Snapshot":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := setup(t)
			t.Cleanup(func() { database.Close(s.DataDir) })
			spool := filepath.Join(s.DataDir, "hook-spool")
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(spool, "1.json")
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.queueDB(); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("import must refuse the retained snapshot at %s: %v", path, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != source {
				t.Fatalf("refused source changed: %q, %v", got, err)
			}
			db, err := database.Open(s.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := db.QueryRow("SELECT count(*) FROM hook_queue").Scan(&rows); err == nil && rows != 0 {
				t.Fatalf("refused event was imported: %d rows", rows)
			}
		})
	}
}
