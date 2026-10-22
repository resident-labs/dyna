package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/profile"
	statewrite "dyna-agent/internal/state"
)

// The runtime GC runs in the engine, so the account record it drops publishes
// the account change that wakes the account refresh pass.
func TestGCPrivateRuntimePublishesAccountChange(t *testing.T) {
	s := testServices(t, false)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(profile.DefaultPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile.DefaultPath(), []byte(`{"profiles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.runtime.Generations, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := &accountstore.Store{Root: s.runtime.Accounts.Root}
	// TODO: legacy/shim fixture — cursor-agent is retired, so GC drops it.
	if err := registry.Update(func(st *accountstore.State) error {
		_, err := st.Save("cursor-agent", "", "identity", "retired", accountstore.Credential{Schema: "fixture-v1", Payload: json.RawMessage(`{}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(registry.Root)
	t.Cleanup(func() { database.Close(root) })
	db, err := database.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	events := hub.New(0)
	statewrite.For(db).SetPublish(events.Publish)
	t.Cleanup(func() { statewrite.For(db).SetPublish(nil) })
	changes, _, unsubscribe := events.Subscribe()
	defer unsubscribe()
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calls["GCPrivateRuntime"](t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	seen := false
	for draining := true; draining; {
		select {
		case ev := <-changes:
			seen = seen || ev.Topic == accountstore.Topic
		default:
			draining = false
		}
	}
	if !seen {
		t.Fatal("runtime GC published no account change")
	}
	st, err := registry.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 0 {
		t.Fatalf("retired accounts are still saved: %+v", st.Entries)
	}
}
