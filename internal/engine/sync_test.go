package engine

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/store"
)

func TestSyncStatusCallReadsSavedStatusInReadOnlyMode(t *testing.T) {
	calls := testCalls(t, true)
	call := calls["SyncStatus"]
	if call == nil {
		t.Fatal("SyncStatus is not registered")
	}
	if err := os.MkdirAll(store.DataDir(), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.DataDir(), "sync.json")
	if err := store.WriteJSON(path, map[string]any{"repositories": lanesync.RemoteStatus{"/repo": {"origin": {Ahead: 2, Behind: 3, Head: "remote-head"}}}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := lanesync.Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(store.DataDir()) })
	result, err := call(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := result.(lanesync.Status)
	if !ok || status["/repo"].Remotes["origin"].Ahead != 2 || status["/repo"].Remotes["origin"].Behind != 3 {
		t.Fatalf("status: %#v", result)
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE lane_sync SET snapshot='bad JSON' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t.Context(), nil); err == nil {
		t.Fatal("invalid saved status did not return an error")
	}
}

func TestSyncOperationCallsAreRegisteredWrites(t *testing.T) {
	for _, method := range []string{"SyncRefresh", "GitExport"} {
		call := testCalls(t, true)[method]
		if call == nil {
			t.Fatalf("%s missing", method)
		}
		if _, err := call(t.Context(), nil); err == nil {
			t.Fatalf("%s accepted a read-only request", method)
		}
	}
}
