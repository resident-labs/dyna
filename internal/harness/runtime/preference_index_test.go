package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/preferences"
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

// The index lives in dyna.db and creates no retained metadata file.
func TestPreferenceIndexLivesInRows(t *testing.T) {
	r, a, home := preferenceFixture(t, "claude-code")
	importClaudePreferences(t, r, a, home)
	ref, ok, err := preferences.For(a.ID)
	if err != nil || !ok || ref.Settings == "" {
		t.Fatalf("reference = %+v, %v, %v", ref, ok, err)
	}
	if _, err := os.Stat(preferenceIndexPath(r)); !os.IsNotExist(err) {
		t.Fatalf("retained index exists: %v", err)
	}
	if values, err := r.PreferenceValues(a.ID); err != nil || values["language"] != "English" {
		t.Fatalf("values = %v, %v", values, err)
	}
}
