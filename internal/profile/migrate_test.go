package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProfileV3MigrationPreservesUnknownFieldsAndACP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	raw := []byte(`{"version":2,"futureRoot":{"keep":42},"profiles":[
 {"name":"codex","harness":"codex","taste":5,"intelligence":5,"cost":5,"futureProfile":["keep"]},
 {"name":"agy","harness":"antigravity","taste":5,"intelligence":5,"cost":5,"output":{"dialect":"antigravity-stream-json","futureOutput":{"keep":true}},"extraArgs":["--effort","high","--keep","--effort=low"]},
 {"name":"custom","harness":"custom","command":["fixture"],"taste":5,"intelligence":5,"cost":5,"output":{"dialect":"codex-jsonl","futureOutput":"keep"}}]}`)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version    int
		FutureRoot map[string]int
		Profiles   []map[string]any
	}
	if err := json.Unmarshal(after, &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != 3 {
		t.Fatalf("version = %d, want 3", document.Version)
	}
	if document.FutureRoot["keep"] != 42 || !reflect.DeepEqual(document.Profiles[0]["futureProfile"], []any{"keep"}) {
		t.Fatal("unknown root or profile field lost")
	}
	for i, want := range []string{"codex-jsonl", "acp-json", "codex-jsonl"} {
		out, ok := document.Profiles[i]["output"].(map[string]any)
		if !ok || out["dialect"] != want {
			t.Fatalf("output %d = %v", i, out)
		}
	}
	agyOutput := document.Profiles[1]["output"].(map[string]any)
	customOutput := document.Profiles[2]["output"].(map[string]any)
	if !reflect.DeepEqual(agyOutput["futureOutput"], map[string]any{"keep": true}) || customOutput["futureOutput"] != "keep" {
		t.Fatal("unknown output fields lost")
	}
	if !reflect.DeepEqual(document.Profiles[1]["extraArgs"], []any{"--keep"}) {
		t.Fatalf("obsolete arguments remain: %v", document.Profiles[1]["extraArgs"])
	}
	backup, err := os.ReadFile(path + ".v2.bak")
	if err != nil || string(backup) != string(raw) {
		t.Fatalf("backup differs: %v", err)
	}
	info, err := os.Stat(path + ".v2.bak")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup is not private: %v", err)
	}
	if err := Migrate(path); err != nil {
		t.Fatal(err)
	}
	replay, err := os.ReadFile(path)
	if err != nil || string(replay) != string(after) {
		t.Fatalf("replay changed bytes: %v", err)
	}
}
