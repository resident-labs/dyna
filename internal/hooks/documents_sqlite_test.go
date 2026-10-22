package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func documentTestService(t *testing.T) Service {
	t.Helper()
	s := Service{DataDir: t.TempDir()}
	if err := statewrite.MigrateMirrors(s.DataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(s.DataDir) })
	return s
}

func TestHookDocumentSurvivesRemovalOfLegacyFile(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error {
		d.Repo = "/main/repo"
		d.Hooks = append(d.Hooks, hook("saved", AfterLand))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.dir("repo"), "hooks.json")); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.load(t.Context(), "repo")
	if err != nil || len(hooks) != 1 || hooks[0].Name != "saved" {
		t.Fatalf("hook document lost with legacy copy: %+v, %v", hooks, err)
	}
	repos, err := s.Repositories(t.Context())
	if err != nil || len(repos) != 1 || repos[0] != "/main/repo" {
		t.Fatalf("repository registry lost with legacy copy: %v, %v", repos, err)
	}
}

func TestHookDocumentsImportLazilyOnRead(t *testing.T) {
	s := documentTestService(t)
	path := filepath.Join(s.dir("repo"), "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old := document{Repo: "/main/repo", Hooks: []Hook{hook("imported", AfterLand)}}
	if err := store.WriteJSON(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(s.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hook_documents").Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup imported %d hook documents: %v", count, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM meta WHERE key='hooks.documents.import'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("startup wrote %d legacy import flags: %v", count, err)
	}
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Name != "imported" {
		t.Fatalf("lazy document import: %+v, %v", list, err)
	}
	writes := statewrite.For(db).Health().Writes
	if _, err := s.load(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	if after := statewrite.For(db).Health().Writes; after != writes {
		t.Fatalf("same file imported twice: %d -> %d", writes, after)
	}
}

func TestDocumentCurrentReadCreatesNoLock(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Repo = "/main/repo"; d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(s.dir("repo"), "hooks.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	db, err := s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	writes := statewrite.For(db).Health().Writes
	if _, err := s.load(t.Context(), "repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Repositories(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("current read recreated hooks.lock: %v", err)
	}
	if after := statewrite.For(db).Health().Writes; after != writes {
		t.Fatalf("current read wrote state: %d -> %d", writes, after)
	}
}

func TestMalformedLegacyHookConfigBlocksReadAfterMigration(t *testing.T) {
	s := documentTestService(t)
	path := filepath.Join(s.dir("repo"), "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(s.DataDir); err != nil {
		t.Fatalf("schema migration parsed hook config: %v", err)
	}
	if list, err := s.load(t.Context(), "repo"); err == nil {
		t.Fatalf("malformed config became an empty successful read: %+v", list)
	}
}

func TestDocumentWriteKeepsValueAfterOlderBinaryFile(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks = []Hook{hook("first", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir("repo"), "hooks.json")
	if err := store.WriteJSON(path, document{Hooks: []Hook{hook("older-binary", AfterLand)}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.update(t.Context(), "repo", func(d *document) error {
		if len(d.Hooks) != 1 || d.Hooks[0].Name != "older-binary" {
			t.Fatalf("write did not import older binary first: %+v", d)
		}
		d.Hooks[0].Name = "new-value"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Name != "new-value" {
		t.Fatalf("older file replaced newer value: %+v, %v", list, err)
	}
}

func TestDocumentFailedUpdateKeepsRowAndCopy(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir("repo"), "hooks.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks[0].Name = "rejected"; return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed write changed rollback copy: %s, %v", after, err)
	}
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Name != "saved" {
		t.Fatalf("failed write changed row: %+v, %v", list, err)
	}
}

func TestConcurrentDocumentEditsKeepEveryHook(t *testing.T) {
	s := documentTestService(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.update(t.Context(), "repo", func(d *document) error {
				d.Hooks = append(d.Hooks, hook(fmt.Sprintf("hook-%d", i), AfterLand))
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 8 {
		t.Fatalf("concurrent edits = %+v, %v", list, err)
	}
}

func TestDocumentNonEngineEmitterReadsForeignFileWithoutImport(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir("repo"), "hooks.json")
	if err := store.WriteJSON(path, document{Hooks: []Hook{hook("older-binary", AfterLand)}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		}
	})
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Name != "older-binary" {
		t.Fatalf("read-only lifecycle lookup = %+v, %v", list, err)
	}
	db, err := s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	if writes := statewrite.For(db).Health().Writes; writes != 0 {
		t.Fatalf("non-engine lookup wrote %d times", writes)
	}
}

func TestNonEngineBeforeLandUsesForeignConfiguration(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%v", malformed), func(t *testing.T) {
			s, repo := setup(t)
			if err := MigrateDocuments(s.DataDir); err != nil {
				t.Fatal(err)
			}
			if err := MigrateRuns(s.DataDir); err != nil {
				t.Fatal(err)
			}
			r, err := repoid.Resolve(t.Context(), repo)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.dir(r.ID), "hooks.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			h := hook("gate", BeforeLand)
			h.OnFail = "block"
			raw, err := json.Marshal(document{Repo: repo, Hooks: []Hook{h}})
			if err != nil {
				t.Fatal(err)
			}
			if malformed {
				raw = []byte("{")
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(s.DataDir); err != nil {
				t.Fatal(err)
			}
			owned := database.OwnsSchema()
			database.DisownSchema()
			t.Cleanup(func() {
				if owned {
					database.OwnSchema()
				}
			})
			called := false
			commandErr := errors.New("gate rejected the change")
			s.Exec = func(context.Context, processguard.Process) error {
				called = true
				return commandErr
			}
			err = s.Emit(t.Context(), Event{Repo: repo, Event: BeforeLand})
			var block *BlockError
			if !errors.As(err, &block) {
				t.Fatalf("foreign configuration did not block: called=%v, error=%v", called, err)
			}
			if malformed {
				if called {
					t.Fatal("malformed configuration executed a command")
				}
			} else if !called || !errors.Is(err, commandErr) {
				t.Fatalf("foreign blocking hook did not run: called=%v, error=%v", called, err)
			}
			db, err := s.documentsDB()
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM hook_documents").Scan(&count); err != nil || count != 0 {
				t.Fatalf("non-engine imported %d documents: %v", count, err)
			}
			foreign, err := statewrite.For(db).Foreign(t.Context(), documentMirrorKey(filepath.Base(s.dir(r.ID))), path)
			if err != nil || !foreign {
				t.Fatalf("non-engine stamped configuration: foreign=%v, error=%v", foreign, err)
			}
		})
	}
}

func TestDocumentWritePublishesNoUnusedEvent(t *testing.T) {
	s := documentTestService(t)
	db, err := s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	statewrite.For(db).SetPublish(func(hub.Event) { published++ })
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	if published != 0 {
		t.Fatalf("document store emitted %d unused events", published)
	}
}

func TestDocumentRegistryOmitsEmptyConfigurations(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Repo = "/main/repo"; return nil }); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repositories(t.Context())
	if err != nil || len(repos) != 0 {
		t.Fatalf("empty configuration registered: %v, %v", repos, err)
	}
	var copy document
	raw, err := os.ReadFile(filepath.Join(s.dir("repo"), "hooks.json"))
	if err != nil || json.Unmarshal(raw, &copy) != nil || copy.Repo != "/main/repo" {
		t.Fatalf("previous release copy: %s, %v", raw, err)
	}
}

func TestDocumentTableRequiresEngineMigration(t *testing.T) {
	s := documentTestService(t)
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	owned := database.OwnsSchema()
	database.DisownSchema()
	t.Cleanup(func() {
		if owned {
			database.OwnSchema()
		}
	})
	if _, err := s.documentsDB(); err == nil {
		t.Fatal("non-engine process created document table")
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='hook_documents'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("non-engine created %d tables, %v", tables, err)
	}
}

func TestDocumentOperationsRespectCancelledContext(t *testing.T) {
	s := documentTestService(t)
	if err := MigrateDocuments(s.DataDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	if err := s.update(ctx, "repo", func(*document) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled update called=%v, error=%v", called, err)
	}
	if _, err := s.load(ctx, "repo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load: %v", err)
	}
	if _, err := s.Repositories(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled registry read: %v", err)
	}
}
