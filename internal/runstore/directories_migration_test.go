package runstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestMigrateKeepsIncompleteDirectoryEvidence(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Setenv(ArchiveDirEnv, "")
	t.Cleanup(func() { database.Close(dataDir) })
	raw := []byte("{\"dir\":\"/retained/worktree\"}\n")
	for _, id := range []string{"wf_evidence", "wf_unknown", "wf_index", "wf_malformed", "wf_oversized"} {
		dir := filepath.Join(dataDir, "runs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(Meta{ID: id, Status: "ok", StartedAt: time.Unix(1, 0), EndedAt: time.Unix(2, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dataDir, "runs", "wf_evidence", "journal.jsonl")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wf_index", "wf_malformed", "wf_oversized"} {
		dir := filepath.Join(dataDir, "runs", id)
		journal := append(append([]byte{}, raw...), []byte("invalid\n")...)
		if id == "wf_oversized" {
			journal = append(append([]byte{}, raw...), bytes.Repeat([]byte("x"), 16<<20)...)
		}
		if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), journal, 0600); err != nil {
			t.Fatal(err)
		}
		if id == "wf_index" {
			if err := os.WriteFile(filepath.Join(dir, DirsFile), []byte("\"/indexed/worktree\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id, want string, complete bool) {
		t.Helper()
		var body string
		var got bool
		if err := db.QueryRow("SELECT body,complete FROM run_directories WHERE run_id=?", id).Scan(&body, &got); err != nil {
			t.Fatal(err)
		}
		if body != want || got != complete {
			t.Fatalf("%s: %s complete=%v", id, body, got)
		}
	}
	check("wf_evidence", `["/retained/worktree"]`, true)
	check("wf_unknown", `[]`, false)
	check("wf_index", `["/indexed/worktree"]`, true)
	check("wf_malformed", `["/retained/worktree"]`, false)
	check("wf_oversized", `["/retained/worktree"]`, false)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("source changed: %v", err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	check("wf_evidence", `["/retained/worktree"]`, true)
}
