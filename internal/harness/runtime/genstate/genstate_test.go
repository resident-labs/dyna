package genstate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/harness/runtime/genstate"
)

// The JSON records earlier builds wrote. The import reads exactly these.
type legacyLeaseFixture struct {
	Version    int             `json:"version"`
	Generation string          `json:"generation"`
	OwnerPID   int             `json:"ownerPid"`
	Released   bool            `json:"released,omitempty"`
	Native     json.RawMessage `json:"native,omitempty"`
}

type legacyViewFixture struct {
	Harness     string
	Home        string
	Generations []string
}

func writeLegacyLease(t *testing.T, dir, name string, lease legacyLeaseFixture) {
	t.Helper()
	raw, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// The import copies the JSON indexes of earlier builds, a later write of an
// old runner replaces its row, and an unreadable record fails the import.
func TestLegacyIndexesImportAndLeasesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "harnesses", "generations")
	generation := filepath.Join(root, "gen-v1-0123456789abcdef")
	usesDir, viewsDir := filepath.Join(root, ".uses"), filepath.Join(root, ".views")
	for _, sub := range []string{usesDir, viewsDir} {
		if err := os.MkdirAll(sub, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacyLease(t, usesDir, "use-abc.json", legacyLeaseFixture{Version: 1, Generation: generation, OwnerPID: 42, Native: json.RawMessage(`{"pid":42}`)})
	legacyView, err := json.Marshal(legacyViewFixture{Harness: "pi", Home: "/home/pi", Generations: []string{generation}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "0123abcd.json"), legacyView, 0600); err != nil {
		t.Fatal(err)
	}

	if err := genstate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	store := genstate.Store{DataDir: dir}
	ctx := context.Background()
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	// A second import keeps the rows as they are.
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	leases, err := store.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].ID != "use-abc.json" || leases[0].Generation != generation || leases[0].OwnerPID != 42 || string(leases[0].Native) != `{"pid":42}` || leases[0].Released {
		t.Fatalf("imported leases = %+v", leases)
	}
	views, err := store.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Harness != "pi" || views[0].Home != "/home/pi" || len(views[0].Generations) != 1 || views[0].Generations[0] != generation {
		t.Fatalf("imported views = %+v", views)
	}

	// An old runner bound the lease after the first import: the file wins.
	writeLegacyLease(t, usesDir, "use-abc.json", legacyLeaseFixture{Version: 1, Generation: generation, OwnerPID: 42, Released: true, Native: json.RawMessage(`{"pid":9}`)})
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	leases, err = store.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || !leases[0].Released || string(leases[0].Native) != `{"pid":9}` {
		t.Fatalf("rebound legacy lease = %+v", leases)
	}

	// A lease records its process, binds a reference, releases, and is deleted.
	lease := genstate.Lease{ID: genstate.NewID(), Generation: generation, OwnerPID: os.Getpid()}
	if err := store.Create(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(ctx, lease.ID, []byte(`{"pid":7}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease.ID); err != nil {
		t.Fatal(err)
	}
	leases, err = store.Leases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 {
		t.Fatalf("leases after bind and release = %+v", leases)
	}
	for _, got := range leases {
		if got.ID == lease.ID && (!got.Released || string(got.Native) != `{"pid":7}`) {
			t.Fatalf("bound lease = %+v", got)
		}
	}
	if err := store.Delete(ctx, lease.ID); err != nil {
		t.Fatal(err)
	}
	if leases, err = store.Leases(ctx); err != nil || len(leases) != 1 {
		t.Fatalf("leases after delete = %+v, %v", leases, err)
	}

	// A pending view keeps the old generation; a complete one replaces it.
	next := filepath.Join(root, "gen-v1-fedcba9876543210")
	if err := store.RecordView(ctx, genstate.View{Harness: "pi", Home: "/home/pi", Generations: []string{next}}, true); err != nil {
		t.Fatal(err)
	}
	views, err = store.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Generations) != 2 {
		t.Fatalf("pending views = %+v", views)
	}
	if err := store.RecordView(ctx, genstate.View{Harness: "pi", Home: "/home/pi", Generations: []string{next}}, false); err != nil {
		t.Fatal(err)
	}
	views, err = store.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Generations) != 1 || views[0].Generations[0] != next {
		t.Fatalf("complete views = %+v", views)
	}
	// The legacy file still names the first generation, but it was imported
	// before the publication, so it must not bring that generation back.
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if views, err = store.Views(ctx); err != nil || len(views) != 1 || len(views[0].Generations) != 1 || views[0].Generations[0] != next {
		t.Fatalf("views after an unchanged legacy import = %+v, %v", views, err)
	}
	// A changed file is a new publication by an earlier build, written after
	// the one this build recorded.
	changed, err := json.Marshal(legacyViewFixture{Harness: "pi", Home: "/home/pi", Generations: []string{generation, next}})
	if err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(viewsDir, "0123abcd.json")
	if err := os.WriteFile(changedPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(time.Second)
	if err := os.Chtimes(changedPath, newer, newer); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if views, err = store.Views(ctx); err != nil || len(views) != 1 || len(views[0].Generations) != 2 {
		t.Fatalf("views after a changed legacy file = %+v, %v", views, err)
	}
	if err := store.DeleteView(ctx, "pi", "/home/pi"); err != nil {
		t.Fatal(err)
	}
	if views, err = store.Views(ctx); err != nil || len(views) != 0 {
		t.Fatalf("views after delete = %+v, %v", views, err)
	}

	// An unreadable protection record fails the import, so cleanup deletes
	// nothing.
	if err := os.WriteFile(filepath.Join(usesDir, "use-broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacy(ctx, root); err == nil {
		t.Fatal("a corrupt legacy lease was imported")
	}
}

// An earlier build can rewrite its view with the same JSON after this build
// published: the rewrite is a new publication and its set wins.
func TestLegacyViewRewriteOfIdenticalJSONWins(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "harnesses", "generations")
	viewsDir := filepath.Join(root, ".views")
	if err := os.MkdirAll(viewsDir, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "gen-v1-0123456789abcdef")
	second := filepath.Join(root, "gen-v1-fedcba9876543210")
	body, err := json.Marshal(legacyViewFixture{Harness: "pi", Home: "/home/pi", Generations: []string{first}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(viewsDir, "pi.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := genstate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	store := genstate.Store{DataDir: dir}
	ctx := context.Background()
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordView(ctx, genstate.View{Harness: "pi", Home: "/home/pi", Generations: []string{second}}, false); err != nil {
		t.Fatal(err)
	}
	// The same bytes written again, later, are a new publication.
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, newer, newer); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	views, err := store.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Generations) != 1 || views[0].Generations[0] != first {
		t.Fatalf("views after a later identical rewrite = %+v", views)
	}
}

// A publication in flight does not supersede a legacy view that this build has
// not imported yet, because the home may still read it if the publication
// fails.
func TestPendingPublicationKeepsAnUnimportedLegacyView(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "harnesses", "generations")
	viewsDir := filepath.Join(root, ".views")
	if err := os.MkdirAll(viewsDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "gen-v1-0123456789abcdef")
	pending := filepath.Join(root, "gen-v1-fedcba9876543210")
	body, err := json.Marshal(legacyViewFixture{Harness: "pi", Home: "/home/pi", Generations: []string{legacy}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(viewsDir, "pi.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := genstate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	store := genstate.Store{DataDir: dir}
	ctx := context.Background()
	if err := store.RecordView(ctx, genstate.View{Harness: "pi", Home: "/home/pi", Generations: []string{pending}}, true); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacy(ctx, root); err != nil {
		t.Fatal(err)
	}
	views, err := store.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || len(views[0].Generations) != 2 {
		t.Fatalf("views during a pending publication = %+v, want both generations", views)
	}
}
