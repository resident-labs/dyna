package pr

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/pr/state"
)

// saveState stores a JSON PR state, as the engine would after a write.
func saveState(t *testing.T, root string, raw []byte) {
	t.Helper()
	var st state.State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(root, st); err != nil {
		t.Fatal(err)
	}
}

func storeRevision(t *testing.T, root string) string {
	t.Helper()
	revision, err := state.Revision(root)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}
