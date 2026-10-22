package workflowtemplate

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fsnotify/fsnotify"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func seedRow(t *testing.T, cat seedCatalog) managedManifest {
	t.Helper()
	db, err := cat.store.db()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readSeedRecord(t.Context(), db, cat.RegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeOldManifest(t *testing.T, registry string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(managedManifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceRegularFile(manifestPath(registry), body, true); err != nil {
		t.Fatal(err)
	}
}

func TestStartupImportsLegacyDefaultManifestOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := DefaultRegistryDir()
	writeOldManifest(t, registry, map[string]string{"echo": contentHash([]byte("return 1\n"))})
	if err := os.WriteFile(filepath.Join(registry, "echo.js"), []byte("return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	if err := state.MigrateMirrors(dir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	cat := seedCatalog{Catalog: New(registry), store: Store{DataDir: dir}}
	manifest := seedRow(t, cat)
	if manifest.Files["echo"] != contentHash([]byte("return 1\n")) {
		t.Fatalf("imported row: %#v", manifest)
	}
	if err := os.Remove(manifestPath(registry)); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 2\n")}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("seed after one-time import: %#v, %v", result, err)
	}
}

func TestSeedImportsPreviouslyUnknownLegacyCustomRegistry(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "custom")
	writeOldManifest(t, registry, map[string]string{"echo": contentHash([]byte("return 1\n"))})
	if err := os.WriteFile(filepath.Join(registry, "echo.js"), []byte("return 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cat := newSeedCatalog(t, registry)
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 2\n")}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("first custom seed: %#v, %v", result, err)
	}
	writeOldManifest(t, registry, map[string]string{"echo": "stale"})
	result, err = cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 3\n")}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("canonical custom seed: %#v, %v", result, err)
	}
}

func TestCommittedRowWinsBeforeRollbackCopyIsWritten(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	db, err := cat.store.db()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the row commit and before the rollback copy.
	manifest := managedManifest{Files: map[string]string{"echo": contentHash([]byte("return 2\n"))}}
	if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		return saveSeedRecord(t.Context(), conn, cat.RegistryDir, manifest)
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cat.RegistryDir, "echo.js"), []byte("return 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 3\n")}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("stale rollback copy replaced committed row: %#v, %v", result, err)
	}
}

func TestSeedDoesNotPublishAndPrunesRemovedStarterHashes(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	if _, err := cat.Seed(fstest.MapFS{"old.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	db, err := cat.store.db()
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	state.For(db).SetPublish(func(hub.Event) { events++ })
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 2\n")}}); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("unused template events = %d, want 0", events)
	}
	manifest := seedRow(t, cat)
	if len(manifest.Files) != 1 || manifest.Files["echo"] == "" {
		t.Fatalf("removed starter hashes remain: %#v", manifest)
	}
	if _, err := os.Stat(filepath.Join(cat.RegistryDir, "old.js")); err != nil {
		t.Fatalf("pruning metadata removed user source: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cat.store.Seed(ctx, cat.Catalog, fstest.MapFS{"new.js": {Data: []byte("return 3\n")}}); err == nil {
		t.Fatal("cancelled seed succeeded")
	}
	if events != 0 {
		t.Fatalf("cancelled write published an event")
	}
}

func TestStartupPreservesRemovedRegistry(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	parked := cat.RegistryDir + "-parked"
	if err := os.Rename(cat.RegistryDir, parked); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(cat.store.DataDir); err != nil {
		t.Fatal(err)
	}
	db, err := cat.store.db()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM workflow_template_seeds WHERE registry=?", cat.RegistryDir).Scan(&count); err != nil || count != 1 {
		t.Fatalf("removed registry rows = %d: %v", count, err)
	}
	// Restore the same files; the canonical hashes must still manage them.
	if err := os.Rename(parked, cat.RegistryDir); err != nil {
		t.Fatal(err)
	}
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 2\n")}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("restored registry kept a stale mirror stamp: %#v, %v", result, err)
	}
}

func TestStoreOpenDoesNotMigrateOldOrNewerSchema(t *testing.T) {
	for _, version := range []string{"0", "1", "3"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() { database.Close(dir) })
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO meta(key,value) VALUES('workflowtemplate.version',?)", version); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(dir); err != nil {
				t.Fatal(err)
			}
			database.DisownSchema()
			t.Cleanup(database.OwnSchema)
			if _, err := (Store{DataDir: dir}).db(); err == nil {
				t.Fatal("store opened an unsupported schema")
			}
			db, err = database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			var after string
			if err := db.QueryRow("SELECT value FROM meta WHERE key='workflowtemplate.version'").Scan(&after); err != nil || after != version {
				t.Fatalf("store open changed version: %q, %v", after, err)
			}
			var tables int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='workflow_template_seeds'").Scan(&tables); err != nil || tables != 0 {
				t.Fatalf("store open created tables: %d, %v", tables, err)
			}
		})
	}
}

func TestEngineSeedReturnsBusyWithoutWaitingForLegacyWriter(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	unlock, err := lockDir(cat.RegistryDir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	_, err = cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}})
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("seed behind a held legacy lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cat.RegistryDir, "echo.js")); !os.IsNotExist(err) {
		t.Fatalf("busy seed wrote source: %v", err)
	}
}

func TestStartupRejectsBusyRequiredRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := DefaultRegistryDir()
	writeOldManifest(t, registry, map[string]string{"echo": "hash"})
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	unlock, err := lockDir(registry)
	if err != nil {
		t.Fatal(err)
	}
	err = Migrate(dir)
	unlock()
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("busy required import: %v", err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	cat := seedCatalog{Catalog: New(registry), store: Store{DataDir: dir}}
	if got := seedRow(t, cat).Files["echo"]; got != "hash" {
		t.Fatalf("retry imported %q", got)
	}
}

func TestSeedIgnoresOldBinaryManifest(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cat.RegistryDir, "echo.js"), []byte("return 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOldManifest(t, cat.RegistryDir, map[string]string{"echo": contentHash([]byte("return 2\n"))})
	next := fstest.MapFS{"echo.js": {Data: []byte("return 3\n")}}
	result, err := cat.Seed(next)
	if err != nil || len(result.Preserved) != 1 {
		t.Fatalf("seed over old binary write: %#v, %v", result, err)
	}
	if _, err := cat.Seed(next); err != nil {
		t.Fatal(err)
	}
	manifest := seedRow(t, cat)
	if manifest.Files["echo"] != contentHash([]byte("return 1\n")) {
		t.Fatalf("foreign manifest replaced the new seed hashes: %#v", manifest)
	}
}

func TestSeedWritesSourceBeforeTakingSQLiteWriter(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	db, err := cat.store.db()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cat.RegistryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(cat.RegistryDir); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- state.For(db).Write(t.Context(), hub.Event{}, func(*sql.Conn) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	seeded := make(chan error, 1)
	go func() {
		_, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}})
		seeded <- err
	}()
	t.Cleanup(func() {
		close(release)
		if err := <-holder; err != nil {
			t.Error(err)
		}
		if err := <-seeded; err != nil {
			t.Error(err)
		}
	})
	// Scheduling can pause the seed goroutine during a full suite. This only
	// bounds a failure; the file-create event remains the success condition.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	path := filepath.Join(cat.RegistryDir, "echo.js")
	for {
		select {
		case event := <-watcher.Events:
			if event.Name == path && event.Op&fsnotify.Create != 0 {
				return
			}
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal("source creation waited for the SQLite writer lock")
		}
	}
}
