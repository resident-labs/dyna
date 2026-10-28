package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife/recordtest"
	defaultstore "dyna-agent/internal/store"
)

// 'harness rm' must name what holds a bundle it refuses to remove: the open
// session that pins it, and the process behind a live use-lease. A released
// lease whose process-guard reference still had a living guardian kept the
// broken 1.4.0-dyna.1 bundle pinned after every session closed (D-399).
func TestBundleRemovalNamesTheHoldingSessionAndProcess(t *testing.T) {
	testAccounts(t)
	if err := os.MkdirAll(filepath.Dir(profile.DefaultPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := defaultstore.WriteJSON(profile.DefaultPath(), map[string]any{"profiles": []any{}}, 0600); err != nil {
		t.Fatal(err)
	}
	manager := manager(t)
	old := install(t, manager, "pi", "1.3.0")
	_ = install(t, manager, "pi", "2.0.0")
	ctx := context.Background()
	recordtest.Write(t, filepath.Join(defaultstore.DataDir(), "sessions"), "ses_holder",
		map[string]any{"id": "ses_holder", "runtimePin": &Pin{Bundle: old}})
	pins, err := OpenPins()
	if err != nil || len(pins) != 1 {
		t.Fatal(pins, err)
	}
	if err = manager.Remove(ctx, "pi", old.Version, pins, true); err == nil || !strings.Contains(err.Error(), "ses_holder") {
		t.Fatalf("removal did not name the holding session: %v", err)
	}
	fakeResourceProbe(t, func(json.RawMessage) (bool, error) { return true, nil })
	lease, err := manager.LeaseManaged(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	reference := `{"socket":"/guard/s","nonce":"` + strings.Repeat("a", 64) + `","guardian":99,"group":4242,"birth":"100"}`
	if err = lease.Bind(json.RawMessage(reference)); err != nil {
		t.Fatal(err)
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err = manager.Remove(ctx, "pi", old.Version, nil, true); err == nil || !strings.Contains(err.Error(), "4242") {
		t.Fatalf("removal did not name the holding process: %v", err)
	}
}
