package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProfileV3MigrationPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	raw := []byte(`{"version":2,"futureRoot":{"keep":42},"profiles":[
 {"name":"codex","harness":"codex","taste":5,"intelligence":5,"cost":5,"futureProfile":["keep"]},
 {"name":"muse","harness":"muse","taste":5,"intelligence":5,"cost":5,"output":{"dialect":"muse-jsonl","futureOutput":{"keep":true}},"extraArgs":["--keep"]},
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
	for i, want := range []string{"codex-jsonl", "muse-jsonl", "codex-jsonl"} {
		out, ok := document.Profiles[i]["output"].(map[string]any)
		if !ok || out["dialect"] != want {
			t.Fatalf("output %d = %v", i, out)
		}
	}
	grokOutput := document.Profiles[1]["output"].(map[string]any)
	customOutput := document.Profiles[2]["output"].(map[string]any)
	if !reflect.DeepEqual(grokOutput["futureOutput"], map[string]any{"keep": true}) || customOutput["futureOutput"] != "keep" {
		t.Fatal("unknown output fields lost")
	}
	if !reflect.DeepEqual(document.Profiles[1]["extraArgs"], []any{"--keep"}) {
		t.Fatalf("extraArgs = %v", document.Profiles[1]["extraArgs"])
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
	t.Run("linked", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target.json")
		link := filepath.Join(t.TempDir(), "profiles.json")
		if err := os.WriteFile(target, raw, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := Migrate(link); err != nil {
				t.Fatal(err)
			}
			if got, err := os.Readlink(link); err != nil || got != target {
				t.Fatalf("link changed: %q, %v", got, err)
			}
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != string(after) {
			t.Fatalf("target differs: %v", err)
		}
		if got, err := os.ReadFile(target + ".v2.bak"); err != nil || string(got) != string(raw) {
			t.Fatalf("target backup differs: %v", err)
		}
	})
	t.Run("failed-backup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profiles.json")
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
		original := profileBackupWrite
		t.Cleanup(func() { profileBackupWrite = original })
		failed := errors.New("injected partial backup write")
		profileBackupWrite = func(f *os.File, b []byte) (int, error) { n, _ := f.Write(b[:3]); return n, failed }
		if err := Migrate(path); !errors.Is(err, failed) {
			t.Fatalf("migration error = %v", err)
		}
		if _, err := os.Lstat(path + ".v2.bak"); !os.IsNotExist(err) {
			t.Errorf("partial backup published: %v", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
			t.Fatalf("failed migration changed source: %v", err)
		}
		profileBackupWrite = original
		if err := Migrate(path); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path + ".v2.bak"); err != nil || string(got) != string(raw) {
			t.Fatalf("retry backup differs: %v", err)
		}
	})

}
