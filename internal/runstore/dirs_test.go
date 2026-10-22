package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A run lists each worker directory once, before the entry that names it, so
// worktree owner lookup never reads the journals.
func TestRunListsWorkerDirectories(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_dirs-listed")
	run, err := Create("dirs", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 1, Cwd: "/w/a", Dir: "/w/b"})
	run.Journal(JournalEntry{ID: 2, Cwd: "/w/a"})
	run.Append(Event{T: "agent_end", Dir: "/w/c"})
	run.Finish("ok", "null", nil)
	b, err := os.ReadFile(filepath.Join(run.Dir, DirsFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\"/w/b\"\n\"/w/a\"\n\"/w/c\"\n"; string(b) != want {
		t.Fatalf("dirs = %q, want %q", b, want)
	}
}

// A run reopened through DYNA_RUN_ID with old entries gets no list, because
// the list would miss the directories those entries name.
func TestReopenedRunWithEntriesGetsNoList(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_dirs-legacy")
	dir := filepath.Join(RunsDir(), "wf_dirs-legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"id":1,"cwd":"/w/old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := Create("legacy", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 2, Cwd: "/w/new"})
	run.Finish("ok", "null", nil)
	if _, err := os.Stat(filepath.Join(dir, DirsFile)); !os.IsNotExist(err) {
		t.Fatalf("reopened run got a partial list: %v", err)
	}
}

// A list that missed a directory would hide an owner, so a failed list write
// deletes the list while the journal entry is still written.
func TestFailedListWriteDeletesList(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_dirs-failed")
	run, err := Create("dirs", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 1, Cwd: "/w/a"})
	// The next list write fails: an empty directory stands at its path.
	list := filepath.Join(run.Dir, DirsFile)
	if err := os.Remove(list); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(list, 0o755); err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 2, Cwd: "/w/b"})
	run.Finish("ok", "null", nil)
	if _, err := os.Stat(filepath.Join(run.Dir, DirsFile)); !os.IsNotExist(err) {
		t.Fatalf("incomplete list kept: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(run.Dir, "journal.jsonl"))
	if err != nil || !strings.Contains(string(b), "/w/b") {
		t.Fatalf("journal lost the entry: %q, %v", b, err)
	}
}

// If the incomplete list cannot be deleted, an entry that names a directory is
// not written, so no journal names a directory the list misses. The next
// entry deletes the list once that works, and is written.
func TestUndeletableStaleListWithholdsEntries(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_RUN_ID", "wf_dirs-stuck")
	run, err := Create("dirs", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 1, Cwd: "/w/a"})
	// The next list write fails, and deleting the list fails too, even for
	// root as in CI: a non-empty directory stands at its path.
	list := filepath.Join(run.Dir, DirsFile)
	if err := os.Remove(list); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(list, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	run.Journal(JournalEntry{ID: 2, Cwd: "/w/hidden"})
	run.Journal(JournalEntry{ID: 3}) // names no directory: still written
	if err := os.RemoveAll(list); err != nil {
		t.Fatal(err)
	}
	journal, _ := os.ReadFile(filepath.Join(run.Dir, "journal.jsonl"))
	if strings.Contains(string(journal), "/w/hidden") || !strings.Contains(string(journal), `"id":3`) {
		t.Fatalf("journal while the stale list stayed: %q", journal)
	}
	run.Journal(JournalEntry{ID: 4, Cwd: "/w/later"})
	run.Finish("ok", "null", nil)
	if _, err := os.Stat(filepath.Join(run.Dir, DirsFile)); !os.IsNotExist(err) {
		t.Fatalf("stale list kept: %v", err)
	}
	journal, _ = os.ReadFile(filepath.Join(run.Dir, "journal.jsonl"))
	if !strings.Contains(string(journal), "/w/later") {
		t.Fatalf("entry after recovery lost: %q", journal)
	}
}
