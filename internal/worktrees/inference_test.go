package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
)

func inferenceTestStore(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return t.TempDir()
}

func inferenceTestWrite(t *testing.T, path string, data any) {
	t.Helper()
	content, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	inferenceTestBytes(t, path, content)
}

func inferenceTestBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInferOwnersSessions(t *testing.T) {
	tree := inferenceTestStore(t)
	closed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, session := range []struct {
		folder, id, name, room, dir string
		closed                      time.Time
	}{
		{"session-a", "session-a", "worker", "project", filepath.Join(tree, "nested", "missing"), time.Time{}},
		{"session-b", "session-b", "", "", tree, closed},
		{"session-c", "session-c", "other", "project", tree + "-sibling", time.Time{}},
		{"session-d", "session-d", "ancestor", "project", filepath.Dir(tree), time.Time{}},
	} {
		recordtest.Write(t, runstore.SessionsDir(), session.folder, map[string]any{
			"id": session.id, "name": session.name, "room": session.room, "dir": session.dir, "closed": session.closed,
		})
	}
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ownerRef{
		{ID: "session-a", Address: "project:worker"},
		{ID: "session-b", Address: "session-b", Closed: true},
	}
	if !reflect.DeepEqual(got[tree].Sessions, want) || len(got[tree].Runs) != 0 {
		t.Fatalf("owners = %#v; want sessions %#v", got, want)
	}
}

func TestInferOwnersRuns(t *testing.T) {
	tree := inferenceTestStore(t)
	ended := "2026-01-01T00:00:00Z"
	for _, run := range []struct {
		id, status, end string
		closed          bool
	}{
		{"wf_ended", "ok", ended, true},
		{"wf_running", "running", ended, false},
		{"wf_no-end", "ok", "0001-01-01T00:00:00Z", false},
	} {
		dir := filepath.Join(runstore.RunsDir(), run.id)
		inferenceTestWrite(t, filepath.Join(dir, "meta.json"), map[string]any{"id": run.id, "status": run.status, "endedAt": run.end})
		// A nested Result must not be mistaken for an ownership pointer.
		inferenceTestWrite(t, filepath.Join(dir, "journal.jsonl"), map[string]any{
			"cwd": filepath.Join(tree, "missing"), "dir": tree, "result": map[string]any{"dir": tree + "-sibling"},
		})
		inferenceTestWrite(t, filepath.Join(dir, "events.jsonl"), map[string]any{"dir": tree})
	}
	other := filepath.Join(runstore.RunsDir(), "wf_unrelated")
	inferenceTestWrite(t, filepath.Join(other, "meta.json"), map[string]any{"id": "wf_unrelated"})
	inferenceTestWrite(t, filepath.Join(other, "journal.jsonl"), map[string]any{"result": map[string]any{"dir": tree}, "cwd": tree + "-sibling"})
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []ownerRef{
		{ID: "wf_ended", Address: "wf_ended", Closed: true},
		{ID: "wf_no-end", Address: "wf_no-end"},
		{ID: "wf_running", Address: "wf_running"},
		{ID: "wf_unrelated", Address: "wf_unrelated"},
	}
	if !reflect.DeepEqual(got[tree].Runs, want) {
		t.Fatalf("runs = %#v; want %#v", got[tree].Runs, want)
	}
}

func TestInferOwnersCanonicalPaths(t *testing.T) {
	tree := inferenceTestStore(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(tree, "escape")); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, runstore.SessionsDir(), "inside", map[string]any{"id": "inside", "dir": filepath.Join(alias, "missing")})
	recordtest.Write(t, runstore.SessionsDir(), "outside", map[string]any{"id": "outside", "dir": filepath.Join(tree, "escape", "missing")})
	got, err := inferOwners(context.Background(), []string{tree, alias}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{tree, alias} {
		if refs := got[path].Sessions; len(refs) != 1 || refs[0].ID != "inside" {
			t.Fatalf("owners for %s = %#v", path, refs)
		}
	}
}

func TestInferOwnersMissingStoresAndMetadata(t *testing.T) {
	tree := inferenceTestStore(t)
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil || len(got[tree].Sessions)+len(got[tree].Runs) != 0 {
		t.Fatalf("missing stores: owners = %#v, error = %v", got, err)
	}
	inferenceTestWrite(t, filepath.Join(runstore.RunsDir(), "orphan", "events.jsonl"), map[string]any{"dir": tree})
	if err := os.MkdirAll(filepath.Join(runstore.SessionsDir(), "missing"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []ownerRef{{ID: "orphan", Address: "orphan"}}; !reflect.DeepEqual(got[tree].Runs, want) {
		t.Fatalf("missing run metadata: owners = %#v", got)
	}
}

func TestInferOwnersRejectsIncompleteEvidence(t *testing.T) {
	for _, test := range []struct {
		name, store, file, data string
	}{
		{"session syntax", "sessions", "", "{"},
		{"session type", "sessions", "", `{"closed":true}`},
		{"session ID mismatch", "sessions", "", `{"id":"other"}`},
		{"session null", "sessions", "", "null"},
		{"session relative path", "sessions", "", `{"dir":"relative"}`},
		{"metadata bound", "sessions", "", strings.Repeat(" ", inferenceMetadataLimit+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := inferenceTestStore(t)
			if test.file == "" {
				recordtest.WriteRaw(t, filepath.Join(store.DataDir(), test.store), "owner", []byte(test.data))
			} else {
				inferenceTestBytes(t, filepath.Join(store.DataDir(), test.store, "owner", test.file), []byte(test.data))
			}
			if _, err := inferOwners(context.Background(), []string{tree}, nil); err == nil {
				t.Fatal("expected an error so cleanup keeps the tree")
			}
		})
	}
}

func TestInferOwnersRejectsUnsafeStorePaths(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			tree := inferenceTestStore(t)
			outside := t.TempDir()
			recordtest.Write(t, outside, "owner", map[string]any{"id": "outside", "dir": tree})
			link, target := filepath.Join(runstore.SessionsDir(), "owner"), filepath.Join(outside, "owner")
			if !directory {
				link, target = recordtest.Path(t, runstore.SessionsDir(), "owner"), recordtest.Path(t, outside, "owner")
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := inferOwners(context.Background(), []string{tree}, nil); err == nil {
				t.Fatal("expected unsafe store symlink to fail closed")
			}
		})
	}
}

func TestInferOwnersDeduplicatesConservatively(t *testing.T) {
	refs := inferenceAddOwner(nil, ownerRef{ID: "same", Closed: true})
	refs = inferenceAddOwner(refs, ownerRef{ID: "same"})
	refs = inferenceAddOwner(refs, ownerRef{ID: "same", Closed: true})
	if len(refs) != 1 || refs[0].Closed {
		t.Fatalf("duplicate evidence hid an open owner: %#v", refs)
	}
}

func TestInferOwnersCanceled(t *testing.T) {
	tree := inferenceTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inferOwners(ctx, []string{tree}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context cancellation", err)
	}
}

func TestInferOwnersIncludesArchivedRunPointers(t *testing.T) {
	tree := inferenceTestStore(t)
	id := "wf_archived"
	archived := filepath.Join(runstore.ArchiveDir(), id)
	inferenceTestWrite(t, filepath.Join(archived, "meta.json"), map[string]any{"id": id, "status": "ok", "endedAt": time.Now()})
	inferenceTestWrite(t, filepath.Join(archived, "journal.jsonl"), map[string]any{"dir": tree})
	if err := os.MkdirAll(runstore.RunsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Rel(runstore.RunsDir(), archived)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(runstore.RunsDir(), id)); err != nil {
		t.Fatal(err)
	}
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[tree].Runs) != 1 || !got[tree].Runs[0].Closed {
		t.Fatalf("archive inference: %#v", got)
	}
}

// A scoped close must not read workflow history when the session owns no
// linked tree. Invalid history makes any accidental read deterministic.
func TestSessionScanSkipsUnneededHistory(t *testing.T) {
	repo := gcRepo(t)
	other := filepath.Join(t.TempDir(), "other")
	git(t, repo, "worktree", "add", "-b", "other", other, "origin/main")
	recordtest.Write(t, runstore.SessionsDir(), "idle", map[string]any{"id": "idle", "dir": repo})
	inferenceTestBytes(t, filepath.Join(runstore.RunsDir(), "broken", "journal.jsonl"), []byte("invalid JSON\n"))
	trees, err := scan(context.Background(), repo, &GCOptions{Session: "idle"})
	if err != nil || len(trees) != 0 {
		t.Fatalf("idle session should inspect no worktrees: %d trees, %v", len(trees), err)
	}
	// Full scans still report ownership errors.
	if trees, err := Scan(context.Background(), repo); err != nil || len(trees) == 0 {
		t.Fatalf("full scan = %#v, %v", trees, err)
	}
}

func TestSessionWorktreePaths(t *testing.T) {
	main := inferenceTestStore(t)
	tree := t.TempDir()
	registered := t.TempDir()
	paths := []string{main, tree, registered}
	records := map[string]Record{registered: {Session: "worker"}}
	for _, test := range []struct {
		name, dir string
		want      []string
	}{
		{"main", main, []string{registered}},
		{"handmade", tree, []string{tree, registered}},
		{"inherited", filepath.Join(tree, "nested"), []string{tree, registered}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{"id": "worker", "dir": test.dir})
			got, err := sessionWorktreePaths(context.Background(), paths, records, "worker")
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("paths = %v, %v; want %v", got, err, test.want)
			}
		})
	}
	recordtest.WriteRaw(t, runstore.SessionsDir(), "worker", []byte("broken"))
	if _, err := sessionWorktreePaths(context.Background(), paths, records, "worker"); err == nil {
		t.Fatal("ignored unreadable session ownership")
	}
}

func TestSessionScanStillChecksWorkflowOwners(t *testing.T) {
	repo := gcRepo(t)
	tree := gcTree(t, repo, "owned", "worker")
	recordtest.Write(t, runstore.SessionsDir(), "worker", map[string]any{"id": "worker", "dir": tree, "closed": time.Now()})
	run := filepath.Join(runstore.RunsDir(), "active")
	inferenceTestWrite(t, filepath.Join(run, "meta.json"), map[string]any{"id": "active", "status": "running"})
	inferenceTestWrite(t, filepath.Join(run, "journal.jsonl"), map[string]any{"cwd": tree})
	trees, err := GC(context.Background(), repo, GCOptions{Session: "worker"})
	if err != nil || len(trees) != 1 || trees[0].Action != "kept" || !strings.Contains(trees[0].Reason, "inferred owner is still open") {
		t.Fatalf("active workflow must protect session tree: %#v, %v", trees, err)
	}
	inferenceTestBytes(t, filepath.Join(run, "journal.jsonl"), []byte("broken"))
	if trees, err := GC(context.Background(), repo, GCOptions{Session: "worker"}); err != nil || len(trees) != 1 || trees[0].Action != "kept" {
		t.Fatalf("rowless run did not protect candidate: %#v, %v", trees, err)
	}
}

// A run folder whose meta.json is broken has no row, so it is an open owner:
// cleanup keeps its tree, and the scan does not fail for every other tree.
func TestInferOwnersKeepsARowlessRunOpen(t *testing.T) {
	for _, body := range []string{"{", `{"id":"other"}`, `{"id":"wf_owner","endedAt":"invalid"}`} {
		tree := inferenceTestStore(t)
		dir := filepath.Join(runstore.RunsDir(), "wf_owner")
		inferenceTestBytes(t, filepath.Join(dir, "meta.json"), []byte(body))
		inferenceTestWrite(t, filepath.Join(dir, "events.jsonl"), map[string]any{"dir": tree})
		got, err := inferOwners(context.Background(), []string{tree}, nil)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if want := []ownerRef{{ID: "wf_owner", Address: "wf_owner"}}; !reflect.DeepEqual(got[tree].Runs, want) {
			t.Fatalf("%s: owners = %#v; want the run open", body, got[tree].Runs)
		}
	}
}
