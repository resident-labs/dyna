package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func runTestService(t *testing.T) Service {
	t.Helper()
	s := Service{DataDir: t.TempDir()}
	if err := statewrite.MigrateMirrors(s.DataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.DataDir) })
	return s
}

func TestRunRecordSurvivesRemovalOfLegacyFile(t *testing.T) {
	s := runTestService(t)
	run := Run{ID: strings.Repeat("a", 32), Repo: "repo", Name: "hook-test", Result: "pass", StartedAt: time.Now().UTC()}
	if _, err := s.saveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.dir(run.Repo), "runs", run.ID+".json")); err != nil {
		t.Fatal(err)
	}
	runs, err := s.runs(t.Context(), run.Repo, "")
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].Result != "pass" {
		t.Fatalf("run lost with legacy copy: %+v, %v", runs, err)
	}
}

func TestRunFilesImportLazilyOnRead(t *testing.T) {
	s := runTestService(t)
	run := Run{ID: strings.Repeat("b", 32), Repo: "repo", Name: "hook-test", Result: "pass"}
	path := filepath.Join(s.dir(run.Repo), "runs", run.ID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, run, 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateRuns(s.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := s.runsDB()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hook_runs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup imported %d run records: %v", count, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM meta WHERE key='hooks.runs.import'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup wrote %d legacy import flags: %v", count, err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("startup created a run lock: %v", err)
	}
	runs, err := s.runs(t.Context(), run.Repo, "")
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("lazy import: %+v, %v", runs, err)
	}
	writes := statewrite.For(db).Health().Writes
	if _, err := s.runs(t.Context(), run.Repo, ""); err != nil {
		t.Fatal(err)
	}
	if after := statewrite.For(db).Health().Writes; after != writes {
		t.Fatalf("read imported the same file again: %d -> %d", writes, after)
	}
}

func TestRunReadDoesNotImportOwnCopyOrPublish(t *testing.T) {
	s := runTestService(t)
	db, err := s.runsDB()
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	statewrite.For(db).SetPublish(func(hub.Event) { published++ })
	run := Run{ID: strings.Repeat("c", 32), Repo: "repo", Name: "hook-test", Result: "pass"}
	if _, err := s.saveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(s.dir(run.Repo), "runs", run.ID+".json.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	writes := statewrite.For(db).Health().Writes
	if _, err := s.runs(t.Context(), run.Repo, ""); err != nil {
		t.Fatal(err)
	}
	if after := statewrite.For(db).Health().Writes; after != writes {
		t.Fatalf("own copy caused another write: %d -> %d", writes, after)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("read recreated a current copy’s lock: %v", err)
	}
	if published != 0 {
		t.Fatalf("run store emitted %d unused events", published)
	}
}

func TestRunCrashBeforeCopyKeepsCommittedRow(t *testing.T) {
	s := runTestService(t)
	run := Run{ID: strings.Repeat("d", 32), Repo: "repo", Name: "hook-test", Result: "pending"}
	if _, err := s.saveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	db, err := s.runsDB()
	if err != nil {
		t.Fatal(err)
	}
	run.Result = "pass"
	key := filepath.Base(s.dir(run.Repo))
	if err := statewrite.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		return writeRun(t.Context(), conn, key, run)
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := s.runs(t.Context(), run.Repo, "")
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("stale copy replaced committed row: %+v, %v", runs, err)
	}
}

func TestRunListFiltersAndOrdersCommittedRecords(t *testing.T) {
	s := runTestService(t)
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"hook-test", "hook-other", "hook-test"} {
		run := Run{ID: strings.Repeat(string(rune('a'+i)), 32), Repo: "repo", Name: name, StartedAt: start.Add(-time.Duration(i) * time.Minute)}
		if _, err := s.saveRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.runs(t.Context(), "repo", "test")
	if err != nil || len(runs) != 2 || runs[0].ID != strings.Repeat("c", 32) || runs[1].ID != strings.Repeat("a", 32) {
		t.Fatalf("ordered filtered runs: %+v, %v", runs, err)
	}
}

func TestRunImportSkipsDamagedFiles(t *testing.T) {
	for _, kind := range []string{"json", "run identity", "repository identity", "file name"} {
		t.Run(kind, func(t *testing.T) {
			s := runTestService(t)
			good := Run{ID: strings.Repeat("a", 32), Repo: "repo", Name: "hook-test", Result: "pass"}
			if _, err := s.saveRun(t.Context(), good); err != nil {
				t.Fatal(err)
			}
			bad := Run{ID: strings.Repeat("b", 32), Repo: "repo", Name: "hook-test"}
			name := bad.ID + ".json"
			switch kind {
			case "run identity":
				bad.ID = strings.Repeat("c", 32)
			case "repository identity":
				bad.Repo = "another repo"
			case "file name":
				name = "bad.json"
			}
			raw, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "json" {
				raw = []byte("{")
			}
			path := filepath.Join(s.dir(good.Repo), "runs", name)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			runs, err := s.runs(t.Context(), good.Repo, "")
			if err != nil || len(runs) != 1 || runs[0].ID != good.ID {
				t.Fatalf("damaged file broke history: %+v, %v", runs, err)
			}
			db, err := s.runsDB()
			if err != nil {
				t.Fatal(err)
			}
			key := runMirrorKey(filepath.Base(s.dir(good.Repo)), strings.TrimSuffix(name, ".json"))
			if foreign, err := statewrite.For(db).Foreign(t.Context(), key, path); err != nil || foreign {
				t.Fatalf("skipped file was not stamped: foreign=%v, %v", foreign, err)
			}
			writes := statewrite.For(db).Health().Writes
			if _, err := s.runs(t.Context(), good.Repo, ""); err != nil {
				t.Fatal(err)
			}
			if after := statewrite.For(db).Health().Writes; after != writes {
				t.Fatalf("skipped file imported again: %d -> %d", writes, after)
			}
			if kept, err := os.ReadFile(path); err != nil || string(kept) != string(raw) {
				t.Fatalf("skipped file changed: %q, %v", kept, err)
			}
		})
	}
}

func TestRunTableRequiresEngineMigration(t *testing.T) {
	s := runTestService(t)
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
	if _, err := s.runsDB(); err == nil || !strings.Contains(err.Error(), "only the engine") {
		t.Fatalf("non-engine run migration: %v", err)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='hook_runs'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("non-engine created %d tables, %v", tables, err)
	}
}

func TestRunWriteKeepsNewValueAfterOlderBinaryFile(t *testing.T) {
	s := runTestService(t)
	run := Run{ID: strings.Repeat("e", 32), Repo: "repo", Name: "hook-test", Result: "pending"}
	if _, err := s.saveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir(run.Repo), "runs", run.ID+".json")
	run.Result = "fail"
	if err := store.WriteJSON(path, run, 0600); err != nil {
		t.Fatal(err)
	}
	run.Result = "pass"
	if _, err := s.saveRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	runs, err := s.runs(t.Context(), run.Repo, "")
	if err != nil || len(runs) != 1 || runs[0].Result != "pass" {
		t.Fatalf("older file replaced newer run value: %+v, %v", runs, err)
	}
}

func TestRunOperationsRespectCancelledContext(t *testing.T) {
	s := runTestService(t)
	if err := MigrateRuns(s.DataDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	run := Run{ID: strings.Repeat("f", 32), Repo: "repo", Name: "hook-test"}
	if _, err := s.saveRun(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled save: %v", err)
	}
	if runs, err := s.runs(t.Context(), run.Repo, ""); err != nil || len(runs) != 0 {
		t.Fatalf("cancelled save committed: %+v, %v", runs, err)
	}
	path := filepath.Join(s.dir(run.Repo), "runs", run.ID+".json")
	if err := store.WriteJSON(path, run, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runs(ctx, run.Repo, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import: %v", err)
	}
}
