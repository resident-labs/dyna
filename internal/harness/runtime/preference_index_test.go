package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/store"
)

// importClaudePreferences imports one safe Claude Code preference for a.
func importClaudePreferences(t *testing.T, r *Runtime, a NativeAccount, home string) {
	t.Helper()
	writePreferenceFixture(t, home, ".claude/settings.json", `{"language":"English"}`)
	p, err := r.PreviewPreferenceImport(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ImportPreferences(a.ID, p.Hash); err != nil {
		t.Fatal(err)
	}
}

func preferenceIndexPath(r *Runtime) string {
	return filepath.Join(filepath.Dir(r.Config.Path), "harness-preferences.json")
}

// The index lives in dyna.db. harness-preferences.json is only the previous
// release's copy: preferences still load without it.
func TestPreferenceIndexLivesInRows(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	importClaudePreferences(t, r, a, home)
	var copied map[string]preferences.Ref
	if err := store.ReadJSON(preferenceIndexPath(r), &copied); err != nil || copied[a.ID].Settings == "" {
		t.Fatalf("copy = %+v, %v; want the account's layer", copied, err)
	}
	if err := os.Remove(preferenceIndexPath(r)); err != nil {
		t.Fatal(err)
	}
	if values, err := r.PreferenceValues(a.ID); err != nil || values["language"] != "English" {
		t.Fatalf("without the copy, values = %v, %v", values, err)
	}
}

// A damaged index from another binary is skipped once (state.ErrSkipForeign):
// the rows stay, and preferences still load.
func TestDamagedPreferenceIndexKeepsTheRows(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	importClaudePreferences(t, r, a, home)
	if err := os.Remove(preferenceIndexPath(r)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preferenceIndexPath(r), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if values, err := r.PreferenceValues(a.ID); err != nil || values["language"] != "English" {
			t.Fatalf("after a damaged index, values = %v, %v", values, err)
		}
	}
}
