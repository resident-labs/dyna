package workflowtemplate

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
)

func TestSeedFreshInstallWritesBundledStarters(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	fsys := fstest.MapFS{
		"alpha.js": {Data: []byte("export const meta = { description: 'alpha' }\nreturn 1\n")},
		"beta.js":  {Data: []byte("return {ok: true}\n")},
	}
	cat := newSeedCatalog(t, registry)
	result, err := cat.Seed(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 2 || result.Written[0] != "alpha" || result.Written[1] != "beta" {
		t.Fatalf("written = %#v", result.Written)
	}
	if len(result.Updated)+len(result.Preserved)+len(result.Unchanged) != 0 {
		t.Fatalf("fresh seed had extra outcomes: %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(registry, "alpha.js"))
	if err != nil || string(got) != string(fsys["alpha.js"].Data) {
		t.Fatalf("alpha.js = %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(registry, "beta.js"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("beta mode = %v %v", info, err)
	}
	assertManagedHash(t, cat, "alpha", fsys["alpha.js"].Data)
}

func TestSeedIdempotentWhenBundleUnchanged(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	fsys := fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}
	cat := newSeedCatalog(t, registry)
	if _, err := cat.Seed(fsys); err != nil {
		t.Fatal(err)
	}
	result, err := cat.Seed(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Unchanged) != 1 || result.Unchanged[0] != "echo" || len(result.Written)+len(result.Updated)+len(result.Preserved) != 0 {
		t.Fatalf("idempotent seed = %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(registry, "echo.js"))
	if err != nil || string(got) != "return 1\n" {
		t.Fatalf("echo.js changed: %q %v", got, err)
	}
}

func TestSeedUpdatesUnchangedManagedCopy(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	old := fstest.MapFS{"echo.js": {Data: []byte("return {v: 1}\n")}}
	cat := newSeedCatalog(t, registry)
	if _, err := cat.Seed(old); err != nil {
		t.Fatal(err)
	}
	next := fstest.MapFS{"echo.js": {Data: []byte("return {v: 2}\n")}}
	result, err := cat.Seed(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Updated) != 1 || result.Updated[0] != "echo" {
		t.Fatalf("managed update = %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(registry, "echo.js"))
	if err != nil || string(got) != "return {v: 2}\n" {
		t.Fatalf("updated bytes = %q %v", got, err)
	}
	assertManagedHash(t, cat, "echo", next["echo.js"].Data)
}

func TestSeedPreservesUserModifiedFile(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	old := fstest.MapFS{"echo.js": {Data: []byte("return {v: 1}\n")}}
	cat := newSeedCatalog(t, registry)
	if _, err := cat.Seed(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "echo.js"), []byte("return {from: 'user'}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := fstest.MapFS{"echo.js": {Data: []byte("return {v: 2}\n")}}
	result, err := cat.Seed(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Preserved) != 1 || result.Preserved[0] != "echo" {
		t.Fatalf("preserve = %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(registry, "echo.js"))
	if err != nil || string(got) != "return {from: 'user'}\n" {
		t.Fatalf("user file overwritten: %q %v", got, err)
	}
}

func TestSeedPreservesUnknownExistingFileWithoutManifest(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "echo.js"), []byte("return {preexisting: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := newSeedCatalog(t, registry).Seed(fstest.MapFS{"echo.js": {Data: []byte("return {bundled: true}\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Preserved) != 1 {
		t.Fatalf("unknown existing file = %#v", result)
	}
	got, _ := os.ReadFile(filepath.Join(registry, "echo.js"))
	if string(got) != "return {preexisting: true}\n" {
		t.Fatalf("preexisting file overwritten: %q", got)
	}
}

func TestSeedRefusesSymlinkDestinationAndManifest(t *testing.T) {
	dir := t.TempDir()
	registry := filepath.Join(dir, "templates")
	if err := os.MkdirAll(registry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside.js"), filepath.Join(registry, "echo.js")); err != nil {
		t.Fatal(err)
	}
	cat := newSeedCatalog(t, registry)
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Preserved) != 1 {
		t.Fatalf("symlink dest = %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(dir, "outside.js")); !os.IsNotExist(err) {
		t.Fatal("seed wrote through a symlink destination")
	}

	if err := os.Remove(filepath.Join(registry, "echo.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(registry, ManagedManifestName)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside-manifest.json"), filepath.Join(registry, ManagedManifestName)); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatalf("unused symlink manifest error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "outside-manifest.json")); !os.IsNotExist(err) {
		t.Fatal("seed wrote through a symlink manifest")
	}
}

func TestSeedSkipsUnsafeBundledNamesAndLeavesNoTmp(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "nested", "templates")
	fsys := fstest.MapFS{
		"ok.js":        {Data: []byte("return 1\n")},
		"-leading.js":  {Data: []byte("return 1\n")},
		"has space.js": {Data: []byte("return 1\n")},
		"not-js.txt":   {Data: []byte("return 1\n")},
		"notes":        {Data: []byte("just notes")},
	}
	result, err := newSeedCatalog(t, registry).Seed(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) != 1 || result.Written[0] != "ok" {
		t.Fatalf("written = %#v", result)
	}
	entries, err := os.ReadDir(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("left temporary file %s", entry.Name())
		}
		if entry.Name() != "ok.js" && entry.Name() != ManagedManifestName {
			t.Fatalf("unexpected registry file %s", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(registry, "-leading.js")); !os.IsNotExist(err) {
		t.Fatal("seed wrote an unsafe bundled name")
	}
}

func TestSeedShippedMatchesRepositoryExamplesAndLists(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	result, err := newSeedCatalog(t, registry).SeedShipped()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Written) < 3 {
		t.Fatalf("shipped written = %#v, want the maintained starters", result.Written)
	}
	for _, name := range []string{"adversarial-review", "judge-panel", "research-design"} {
		found := false
		for _, got := range result.Written {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing shipped starter %s in %#v", name, result.Written)
		}
		repo, err := os.ReadFile(filepath.Join("..", "..", "examples", name+".js"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(registry, name+".js"))
		if err != nil || string(got) != string(repo) {
			t.Fatalf("%s did not match examples/: %v", name, err)
		}
	}
	list, err := New(registry).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(result.Written) {
		t.Fatalf("list = %#v, seeded = %#v", list, result.Written)
	}
	for _, item := range list {
		if item.Source != SourceUser {
			t.Fatalf("list reported non-user source: %#v", item)
		}
	}
}

func assertManagedHash(t *testing.T, cat seedCatalog, name string, data []byte) {
	t.Helper()
	manifest := seedRow(t, cat)
	if manifest.Files[name] != contentHash(data) {
		t.Fatalf("managed hash for %s = %q, want %s", name, manifest.Files[name], contentHash(data))
	}
}

func TestSeedKeepsHashesWhenRollbackManifestIsRemoved(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "templates")
	cat := newSeedCatalog(t, registry)
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestPath(registry)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 2\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Updated) != 1 {
		t.Fatalf("SQLite seed hash was lost with the rollback copy: %#v", result)
	}
}

type seedCatalog struct {
	Catalog
	store Store
}

func newSeedCatalog(t *testing.T, registry string) seedCatalog {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(func() { database.Close(dataDir) })
	if err := state.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	return seedCatalog{Catalog: New(registry), store: Store{DataDir: dataDir}}
}

func (c seedCatalog) Seed(fsys fs.FS) (SeedResult, error) {
	return c.store.Seed(context.Background(), c.Catalog, fsys)
}

func (c seedCatalog) SeedShipped() (SeedResult, error) {
	return c.store.SeedShipped(context.Background(), c.Catalog)
}
