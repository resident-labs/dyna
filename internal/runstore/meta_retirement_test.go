package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalRunIgnoresRetainedPause(t *testing.T) {
	run := newTestRun(t, "wf_retired-mirrors")
	stale, err := json.Marshal(run.Meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPaused(run.Meta.ID, true); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	for _, name := range []string{"meta.json", "paused"} {
		if _, err := os.Stat(filepath.Join(run.Dir, name)); !os.IsNotExist(err) {
			t.Fatalf("new run wrote retired %s: %v", name, err)
		}
	}
	for name, body := range map[string][]byte{"meta.json": stale, "paused": []byte("paused\n")} {
		if err := os.WriteFile(filepath.Join(run.Dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := ReadMeta(run.Meta.ID); err != nil || m.Status != "ok" || m.Paused || IsPaused(run.Meta.ID) {
		t.Fatalf("canonical read: %+v, %v", m, err)
	}
	if list, err := List(); err != nil || len(list) != 1 || list[0].Status != "ok" {
		t.Fatalf("canonical list: %+v, %v", list, err)
	}
	if _, err := ArchiveRun(run.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := ForceStatus(run.Meta.ID, "error", "late"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(RunDir(run.Meta.ID), "meta.json")); err != nil || string(got) != string(stale) {
		t.Fatalf("canonical write changed retained metadata: %q, %v", got, err)
	}
}
