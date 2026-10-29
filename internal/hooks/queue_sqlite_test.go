package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/processguard"
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

// A previous release left pending events as files under hook-spool. Every
// drain imports what remains in sequence order, and only then removes each
// file; a later enqueue lands after every imported row.
func TestImportMovesLegacySpoolIntoTheQueueInOrder(t *testing.T) {
	t.Run("order and files", func(t *testing.T) {
		s, repo := setup(t)
		t.Cleanup(func() { database.Close(s.DataDir) })
		spool := filepath.Join(s.DataDir, "hook-spool")
		if err := os.MkdirAll(spool, 0700); err != nil {
			t.Fatal(err)
		}
		// Listed in reverse order, and beside files the import must leave.
		writeLegacySpool(t, spool, "00000000000000000007.json", Event{Repo: repo, Worktree: repo, Event: AfterLand, Branch: "second"})
		writeLegacySpool(t, spool, "00000000000000000003.json", Event{Repo: repo, Worktree: repo, Event: AfterLand, Branch: "first"})
		if err := os.WriteFile(filepath.Join(spool, "notes.txt"), []byte("not an event"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.importSpoolFiles(context.Background()); err != nil {
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
		if err := s.Enqueue(Event{Repo: repo, Worktree: repo, Event: AfterLand, Branch: "third"}); err != nil {
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
		writeLegacySpool(t, spool, "00000000000000000001.json", Event{Repo: repo, Worktree: repo, Event: AfterLand})
		if err := s.importSpoolFiles(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(spool); !os.IsNotExist(err) {
			t.Fatalf("emptied legacy spool directory remains: %v", err)
		}
	})
}

// A late legacy file, written after an earlier import already ran, joins the
// queue on the next import: old emitters cannot strand events.
func TestImportPicksUpALateLegacyFile(t *testing.T) {
	s, repo := setup(t)
	t.Cleanup(func() { database.Close(s.DataDir) })
	spool := filepath.Join(s.DataDir, "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	writeLegacySpool(t, spool, "00000000000000000001.json", Event{Repo: repo, Worktree: repo, Event: AfterLand, Branch: "first"})
	if err := s.importSpoolFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	// An old emitter recreates the directory and appends after the queue
	// store already exists.
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	writeLegacySpool(t, spool, "00000000000000000002.json", Event{Repo: repo, Worktree: repo, Event: AfterLand, Branch: "late"})
	if err := s.importSpoolFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, err := s.QueuedEvents(context.Background())
	if err != nil || len(events) != 2 || events[0].Branch != "first" || events[1].Branch != "late" {
		t.Fatalf("queue after late legacy file: %+v, %v", events, err)
	}
}

// A retained snapshot marker in any case, a marker that is not a boolean,
// and a file that is not an event are quarantined beside the queue; the
// events behind them still import.
func TestImportQuarantinesRetainedSnapshotMarkers(t *testing.T) {
	for name, source := range map[string]string{
		"marker":       `{"event":"before-land","snapshot":true}`,
		"first of two": `{"Snapshot":true,"snapshot":false}`,
		"non-boolean":  `{"event":"before-land","SNAPSHOT":null}`,
		"not an event": `[1,2,3]`,
	} {
		t.Run(name, func(t *testing.T) {
			s, repo := setup(t)
			t.Cleanup(func() { database.Close(s.DataDir) })
			spool := filepath.Join(s.DataDir, "hook-spool")
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(spool, "1.json"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			writeLegacySpool(t, spool, "2.json", Event{Repo: repo, Worktree: repo, Event: AfterLand})
			if err := s.importSpoolFiles(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(spool, "1.json.bad")); err != nil || string(got) != source {
				t.Fatalf("quarantine: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(spool, "1.json")); !os.IsNotExist(err) {
				t.Fatalf("refused source remains: %v", err)
			}
			events, err := s.QueuedEvents(context.Background())
			if err != nil || len(events) != 1 {
				t.Fatalf("queue behind the refused file: %+v, %v", events, err)
			}
		})
	}
}

// A drain imports the remaining legacy files before it dispatches, runs
// them with the queued rows, and removes the emptied directory.
func TestDrainImportsLegacySpoolFiles(t *testing.T) {
	s, repo := setup(t)
	add(t, s, repo, hook("queued", AfterLand))
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		return nil
	}
	spool := filepath.Join(s.DataDir, "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	writeLegacySpool(t, spool, "00000000000000000001.json", Event{Repo: repo, Worktree: repo, Event: AfterLand})
	if _, _, err := s.Drain(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if events := queuedEvents(t, s); len(events) != 0 {
		t.Fatalf("drain left imported events queued: %+v", events)
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatalf("emptied legacy spool directory remains: %v", err)
	}
}
