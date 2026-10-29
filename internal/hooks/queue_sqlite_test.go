package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	t.Run("directory and lock retained", func(t *testing.T) {
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
		// The lock file is never unlinked while an old emitter may hold or
		// wait on it; a coordinated cutover removes the directory later.
		for _, kept := range []string{spool, filepath.Join(spool, "write.lock")} {
			if _, err := os.Stat(kept); err != nil {
				t.Fatalf("%s not retained: %v", kept, err)
			}
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
	// An old emitter appends after the queue store already exists; the
	// retained directory is still there.
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
// and a file that is not an event refuse the import: the refused file stays
// in place, in full, for the approved old-emitter drain that clears it, and
// the events behind it wait, as the old startup validator did.
func TestImportRefusesWhatItCannotRun(t *testing.T) {
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
			path := filepath.Join(spool, "1.json")
			if err := s.importSpoolFiles(context.Background()); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("import must refuse %s: %v", path, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != source {
				t.Fatalf("refused source changed: %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(spool, "2.json")); err != nil {
				t.Fatalf("later file left behind: %v", err)
			}
			events, err := s.QueuedEvents(context.Background())
			if err != nil || len(events) != 0 {
				t.Fatalf("queue behind the refused file: %+v, %v", events, err)
			}
		})
	}
}

// A drain imports the remaining legacy files before it dispatches, runs
// them with the queued rows, and keeps the directory and its lock file.
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
	if _, err := os.Stat(filepath.Join(spool, "00000000000000000001.json")); !os.IsNotExist(err) {
		t.Fatalf("imported legacy file remains: %v", err)
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("legacy spool directory not retained: %v", err)
	}
}

// The store watcher's version changes when a legacy spool file appears with
// no queue change, so a late file from a previous-release emitter wakes the
// drain that imports it, and again once the import has cleared the files.
func TestSpoolVersionWakesForLegacyFiles(t *testing.T) {
	s, repo := setup(t)
	t.Cleanup(func() { database.Close(s.DataDir) })
	before := s.SpoolVersion()
	spool := filepath.Join(s.DataDir, "hook-spool")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	writeLegacySpool(t, spool, "00000000000000000001.json", Event{Repo: repo, Worktree: repo, Event: AfterLand})
	appeared := s.SpoolVersion()
	if appeared == "" || appeared == before {
		t.Fatalf("legacy file did not change the wake version: %q then %q", before, appeared)
	}
	if err := s.importSpoolFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if emptied := s.SpoolVersion(); emptied == "" || emptied == appeared {
		t.Fatalf("emptied spool did not change the wake version: %q then %q", appeared, emptied)
	}
}
