package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
)

func writeOwnerFile(t *testing.T, dir, name string, body map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A removal recheck reads sessions again: a session that started using the
// tree after the first scan keeps it, even with no live process in it.
func TestRecheckSeesNewOpenOwner(t *testing.T) {
	repo := gcRepo(t)
	path := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "late-owner", path, "origin/main")
	history := &runHistory{}
	if _, err := scan(context.Background(), repo, &GCOptions{Path: path, history: history}); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, runstore.SessionsDir(), "ses_late", map[string]any{"id": "ses_late", "name": "late", "room": "test", "dir": path})
	trees, err := scan(context.Background(), repo, &GCOptions{Path: path, history: history})
	if err != nil || len(trees) != 1 {
		t.Fatalf("recheck = %#v, %v", trees, err)
	}
	if got := decide(trees[0], func(string) (bool, error) { return true, nil }); got.Action != "kept" || !strings.Contains(got.Reason, "inferred owner is still open") {
		t.Fatalf("recheck missed the new open owner: %#v", got)
	}
}

// Session record versions identify immutable metadata. A recheck reuses the
// decoded ownership projection while those versions stay unchanged.
func TestRecheckReusesUnchangedSessionHistory(t *testing.T) {
	tree := inferenceTestStore(t)
	id := "ses_closed"
	recordtest.Write(t, runstore.SessionsDir(), id, map[string]any{
		"id": id, "dir": tree, "closed": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	history := &runHistory{}
	first, err := inferOwners(context.Background(), []string{tree}, history)
	if err != nil || len(first[tree].Sessions) != 1 || !first[tree].Sessions[0].Closed {
		t.Fatalf("first inference = %#v, %v", first, err)
	}
	// Change only the bytes, not the version, to make another decode fail.
	// Canonical writers always change the version with the metadata.
	db, err := database.Open(filepath.Dir(runstore.SessionsDir()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_records SET meta = '{' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	again, err := inferOwners(context.Background(), []string{tree}, history)
	if err != nil || len(again[tree].Sessions) != 1 || again[tree].Sessions[0].ID != id {
		t.Fatalf("recheck decoded an unchanged session record: %#v, %v", again, err)
	}
}

// A recheck follows session moves and reopening, and damaged new metadata
// remains an error instead of falling back to the cached ownership evidence.
func TestRecheckReadsChangedSessionHistory(t *testing.T) {
	tree := inferenceTestStore(t)
	moved := t.TempDir()
	id := "ses_moving"
	write := func(dir string, closed time.Time) {
		t.Helper()
		recordtest.Write(t, runstore.SessionsDir(), id, map[string]any{"id": id, "dir": dir, "closed": closed})
	}
	write(tree, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	history := &runHistory{}
	if _, err := inferOwners(context.Background(), []string{tree, moved}, history); err != nil {
		t.Fatal(err)
	}
	write(moved, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	got, err := inferOwners(context.Background(), []string{tree, moved}, history)
	if err != nil || len(got[tree].Sessions) != 0 || len(got[moved].Sessions) != 1 || !got[moved].Sessions[0].Closed {
		t.Fatalf("session move = %#v, %v", got, err)
	}
	write(moved, time.Time{})
	got, err = inferOwners(context.Background(), []string{moved}, history)
	if err != nil || len(got[moved].Sessions) != 1 || got[moved].Sessions[0].Closed {
		t.Fatalf("reopened session = %#v, %v", got, err)
	}
	recordtest.WriteRaw(t, runstore.SessionsDir(), id, []byte("{"))
	if _, err := inferOwners(context.Background(), []string{moved}, history); err == nil {
		t.Fatal("recheck ignored damaged changed metadata")
	}
}

// Ended runs never change, so a recheck reuses what their journals said and
// does not read them again. That second read spent cleanup's time budget.
func TestRecheckReusesEndedRunHistory(t *testing.T) {
	repo := gcRepo(t)
	path := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "ended-run", path, "origin/main")
	run := filepath.Join(runstore.RunsDir(), "wf_ended")
	writeOwnerFile(t, run, "meta.json", map[string]any{"id": "wf_ended", "status": "ok", "endedAt": "2026-01-01T00:00:00Z"})
	writeOwnerFile(t, run, "journal.jsonl", map[string]any{"id": 1, "cwd": path})
	history := &runHistory{}
	first, err := scan(context.Background(), repo, &GCOptions{Path: path, history: history})
	if err != nil || len(first) != 1 || len(first[0].inferred.Runs) != 1 {
		t.Fatalf("first scan = %#v, %v", first, err)
	}
	// Canonical evidence remains valid if the legacy journal changes.
	if err := os.WriteFile(filepath.Join(run, "journal.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := scan(context.Background(), repo, &GCOptions{Path: path, history: history})
	if err != nil || len(again) != 1 || len(again[0].inferred.Runs) != 1 || again[0].inferred.Runs[0].ID != "wf_ended" {
		t.Fatalf("recheck reread the ended run: %#v, %v", again, err)
	}
}

// Every early exit of remove marks a tree kept for lack of time as Deferred,
// so session cleanup posts no notice about it.
func TestRemoveDefersWhenTimeRunsOut(t *testing.T) {
	repo := gcRepo(t)
	path := filepath.Join(t.TempDir(), "tree")
	git(t, repo, "worktree", "add", "-b", "late", path, "origin/main")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := remove(ctx, repo, Tree{Path: path, Action: "would remove"}, &runHistory{}, func(string) (bool, error) { return true, nil }, nil)
	if got.Action != "kept" || !got.Deferred {
		t.Fatalf("timed-out removal = %#v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("tree removed: %v", err)
	}
}
