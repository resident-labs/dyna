package genstate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/runtime/genstate"
)

// The one-time import reads the JSON indexes of earlier builds, and the store
// then owns the same records through their whole lifecycle.
func TestLegacyIndexesImportAndLeasesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "harnesses", "generations")
	generation := filepath.Join(root, "gen-v1-0123456789abcdef")
	for _, sub := range []string{".uses", ".views"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	legacyLease, err := json.Marshal(map[string]any{"version": 1, "generation": generation, "ownerPid": 42, "native": json.RawMessage(`{"pid":42}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".uses", "use-abc.json"), legacyLease, 0600); err != nil {
		t.Fatal(err)
	}
	legacyView, err := json.Marshal(map[string]any{"Harness": "pi", "Home": "/home/pi", "Generations": []string{generation}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".views", "0123abcd.json"), legacyView, 0600); err != nil {
		t.Fatal(err)
	}

	if err := genstate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	// A second migration keeps the imported rows as they are.
	if err := genstate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	store := genstate.Store{DataDir: dir}
	ctx := context.Background()
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
	if err := store.DeleteView(ctx, "pi", "/home/pi"); err != nil {
		t.Fatal(err)
	}
	if views, err = store.Views(ctx); err != nil || len(views) != 0 {
		t.Fatalf("views after delete = %+v, %v", views, err)
	}
}
