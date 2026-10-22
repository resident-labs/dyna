package library

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/library"
	"dyna-agent/internal/store/storetest"
)

// A remote identifies this real repository without creating a commit.
func authorityRepo(t *testing.T) string {
	t.Helper()
	storetest.Isolate(t)
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	config := filepath.Join(dir, ".git", "config")
	file, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[remote \"origin\"]\n\turl = https://example.com/library.git\n")
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return dir
}

func TestLibraryCLIRejectsWorkerMutations(t *testing.T) {
	data, repo := t.TempDir(), authorityRepo(t)
	t.Setenv("DYNA_MODE", "workflow")
	t.Setenv("DYNA_SESSION", "worker")
	serveLibrary(t, data, authority.Current())
	c := Command(Options{DataDir: data})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"add", "worker-entry", "--name", "Worker", "--summary", "Worker.", "--owns", "agents/**", "--repo", repo})
	if err := c.Execute(); err == nil || !strings.HasPrefix(err.Error(), "library add is ") {
		t.Fatalf("worker mutation: %v", err)
	}
}

func TestLibraryCLISavedLibrarianWrites(t *testing.T) {
	data, repo := t.TempDir(), authorityRepo(t)
	if _, err := (librarianstate.Store{DataDir: data}).Update("example.com/library", true, func(st *librarianstate.State) error { st.Session = "librarian"; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "librarian")
	if out := runner(t, data, repo)("add", "saved-entry", "--name", "Saved", "--summary", "Saved.", "--owns", "agents/**"); !strings.Contains(out, "saved-entry") {
		t.Fatal(out)
	}
}

func TestLibraryCLIOtherSessionReads(t *testing.T) {
	data, repo := t.TempDir(), authorityRepo(t)
	s := library.Store{DataDir: data, Actor: authority.Actor{Role: authority.Owner}}
	if _, err := s.Add("example.com/library", library.Entry{ID: "saved-entry", Name: "Saved", Kind: "feature", Summary: "Saved.", Owns: []string{"agents/**"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", "reader")
	if out := runner(t, data, repo)("list"); !strings.Contains(out, "saved-entry") {
		t.Fatal(out)
	}
}
