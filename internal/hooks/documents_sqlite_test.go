package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/repoid"
	statewrite "dyna-agent/internal/state"
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

func TestHookDocumentNeedsNoLegacyFile(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error {
		d.Repo = "/main/repo"
		d.Hooks = append(d.Hooks, hook("saved", AfterLand))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir("repo"), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("created hook copy: %v", err)
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

func TestHookDocumentImportMakesSQLAuthoritative(t *testing.T) {
	s := documentTestService(t)
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "hooks.documents", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(t.Context(), documentsSchema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	write := func(key, raw string) string {
		path := filepath.Join(s.DataDir, "repos", key, "hooks.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	raw := `{"repo":"/main/imported","future":9007199254740993,"hooks":[{"name":"imported","event":"after-land","command":"true"}]}`
	key := filepath.Base(s.dir("repo"))
	path := write(key, raw)
	empty := `{"repo":"/main/empty","hooks":[]}`
	if _, err := db.Exec("INSERT INTO hook_documents(repo,body) VALUES('empty',?)", empty); err != nil {
		t.Fatal(err)
	}
	write("empty", "{obsolete malformed source")
	owned := write("owned", "{}")
	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}
	if err := statewrite.For(db).Mirror(t.Context(), documentMirrorKey("owned"), owned, func(out io.Writer) error { _, err := io.WriteString(out, raw); return err }); err != nil {
		t.Fatal(err)
	}
	var stamp string
	if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key=?", documentMirrorKey("owned")).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(s.DataDir); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow("SELECT body FROM hook_documents WHERE repo=?", key).Scan(&got); err != nil || got != raw {
		t.Fatalf("immediate import = %s, %v", got, err)
	}
	if err := db.QueryRow("SELECT body FROM hook_documents WHERE repo='empty'").Scan(&got); err != nil || got != empty {
		t.Fatalf("empty row replaced: %s, %v", got, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hook_documents WHERE repo='owned'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("owned absence restored: %d, %v", count, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != raw {
		t.Fatalf("source changed: %s, %v", got, err)
	}
	var after string
	if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key=?", documentMirrorKey("owned")).Scan(&after); err != nil || after != stamp {
		t.Fatalf("stamp changed: %s, %v", after, err)
	}
	changed := `{"repo":"/main/imported","hooks":[{"name":"obsolete","event":"after-land","command":"true"}]}`
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.update(t.Context(), "repo", func(d *document) error {
		if len(d.Hooks) != 1 || d.Hooks[0].Name != "imported" {
			t.Fatalf("source replaced SQL: %+v", d)
		}
		d.Hooks[0].Name = "canonical"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(s.DataDir); err != nil {
		t.Fatal(err)
	}
	list, err := s.load(t.Context(), "repo")
	if err != nil || len(list) != 1 || list[0].Name != "canonical" {
		t.Fatalf("reopen = %+v, %v", list, err)
	}
	repos, err := s.Repositories(t.Context())
	if err != nil || len(repos) != 1 || repos[0] != "/main/imported" {
		t.Fatalf("repositories = %v, %v", repos, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != changed {
		t.Fatalf("update changed source: %s, %v", got, err)
	}
	db, err = s.documentsDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key=?", documentMirrorKey("owned")).Scan(&after); err != nil || after != stamp {
		t.Fatalf("update changed stamp: %s, %v", after, err)
	}

}

func TestDocumentCurrentReadCreatesNoLock(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Repo = "/main/repo"; d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(s.dir("repo"), "hooks.lock")
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

func TestMalformedLegacyHookConfigBlocksMigration(t *testing.T) {
	s := documentTestService(t)
	path := filepath.Join(s.dir("repo"), "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDocuments(s.DataDir); err == nil {
		t.Fatal("migration accepted malformed hook configuration")
	}
}

func TestDocumentFailedUpdateKeepsRow(t *testing.T) {
	s := documentTestService(t)
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks = []Hook{hook("saved", AfterLand)}; return nil }); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("refused")
	if err := s.update(t.Context(), "repo", func(d *document) error { d.Hooks[0].Name = "rejected"; return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
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

func TestNonEngineBeforeLandUsesCanonicalConfiguration(t *testing.T) {
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
			if err := s.update(t.Context(), r.ID, func(d *document) error { d.Repo = repo; d.Hooks = []Hook{h}; return nil }); err != nil {
				t.Fatal(err)
			}
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
			if !called || !errors.Is(err, commandErr) {
				t.Fatalf("canonical hook did not run: called=%v, error=%v", called, err)
			}
			db, err := s.documentsDB()
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM hook_documents").Scan(&count); err != nil || count != 1 {
				t.Fatalf("canonical document count %d documents: %v", count, err)
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
	if _, err := os.Stat(filepath.Join(s.dir("repo"), "hooks.json")); !os.IsNotExist(err) {
		t.Fatalf("created hook copy: %v", err)
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
