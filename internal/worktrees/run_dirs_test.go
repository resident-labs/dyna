package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dyna-agent/internal/runstore"
)

// Owner lookup reads a run's small worker directory list, never its journals.
// The journals hold every prompt and result and made each scan take seconds.
func TestInferOwnersReadsRunDirectoryList(t *testing.T) {
	tree := inferenceTestStore(t)
	run := filepath.Join(runstore.RunsDir(), "wf_listed")
	inferenceTestWrite(t, filepath.Join(run, "meta.json"), map[string]any{"id": "wf_listed", "status": "running"})
	inferenceTestBytes(t, filepath.Join(run, "dirs.jsonl"), []byte(`"`+tree+`"`+"\n"))
	// The journal is not read: invalid content would fail a journal read.
	inferenceTestBytes(t, filepath.Join(run, "journal.jsonl"), []byte("not json\n"))
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[tree].Runs) != 1 || got[tree].Runs[0].ID != "wf_listed" || got[tree].Runs[0].Closed {
		t.Fatalf("owners = %#v", got[tree])
	}
}

// A run from before the list existed is read from its journals once. An
// ended run then gets the list; a running one is read again next time.
func TestInferOwnersBuildsListForEndedLegacyRun(t *testing.T) {
	tree := inferenceTestStore(t)
	for _, run := range []struct {
		id    string
		meta  map[string]any
		write bool
	}{
		{"wf_ended", map[string]any{"id": "wf_ended", "status": "ok", "endedAt": "2026-01-01T00:00:00Z"}, true},
		{"wf_running", map[string]any{"id": "wf_running", "status": "running"}, false},
	} {
		dir := filepath.Join(runstore.RunsDir(), run.id)
		inferenceTestWrite(t, filepath.Join(dir, "meta.json"), run.meta)
		inferenceTestWrite(t, filepath.Join(dir, "journal.jsonl"), map[string]any{"id": 1, "cwd": tree})
	}
	got, err := inferOwners(context.Background(), []string{tree}, nil)
	if err != nil || len(got[tree].Runs) != 2 {
		t.Fatalf("owners = %#v, %v", got[tree], err)
	}
	if b, err := os.ReadFile(filepath.Join(runstore.RunsDir(), "wf_ended", "dirs.jsonl")); err != nil || string(b) != `"`+tree+`"`+"\n" {
		t.Fatalf("ended run list = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(runstore.RunsDir(), "wf_running", "dirs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("running legacy run got a list: %v", err)
	}
}

// Concurrent passes that publish the same run's list never leave an empty or
// partial list for a reader.
func TestConcurrentListPublicationIsComplete(t *testing.T) {
	inferenceTestStore(t)
	run := "wf_race"
	if err := os.MkdirAll(filepath.Join(runstore.RunsDir(), run), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(runstore.RunsDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	dirs := []string{"/w/a", "/w/b"}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, found, err := inferenceDirs(context.Background(), root, filepath.Join(run, runstore.DirsFile))
			if err != nil || found && len(got) != len(dirs) {
				select {
				case bad <- fmt.Sprintf("reader saw %q, %v", got, err):
				default:
				}
			}
		}
	}()
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := inferenceWriteDirs(root, run, dirs); err != nil {
				select {
				case bad <- err.Error():
				default:
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	select {
	case msg := <-bad:
		t.Fatal(msg)
	default:
	}
	entries, _ := os.ReadDir(filepath.Join(runstore.RunsDir(), run))
	for _, e := range entries {
		if e.Name() != runstore.DirsFile {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}
