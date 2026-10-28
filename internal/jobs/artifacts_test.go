package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A job's named outputs outlive its folder on the node: they are copied to
// the engine, a directory keeps its tree, and a path the job never made is
// named in the result instead of failing the copy.
func TestArtifactsAreKeptAfterTheJob(t *testing.T) {
	jobDir := t.TempDir()
	built := filepath.Join(jobDir, "work", "target", "release")
	if err := os.MkdirAll(built, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(built, "app"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "artifacts")
	job := Job{Dir: jobDir, Artifacts: []string{"target/release", "missing.txt"}}
	note, err := copyArtifacts(t.Context(), Executor{Node: Conn{Name: "here"}}, job, dest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "target", "release", "app"))
	if err != nil || string(got) != "binary" {
		t.Fatalf("kept artifact = %q, %v; want the built file", got, err)
	}
	if !strings.Contains(note, "missing.txt") {
		t.Fatalf("note = %q, want it to name the missing artifact", note)
	}
}

// An artifact path stays inside the job's work folder.
func TestArtifactPathsStayInTheWorkFolder(t *testing.T) {
	for _, bad := range []string{"", "/etc/passwd", "../x", "a/../../x", "."} {
		if err := checkArtifact(bad); err == nil {
			t.Fatalf("checkArtifact(%q) = nil, want an error", bad)
		}
	}
	if err := checkArtifact("target/release/app"); err != nil {
		t.Fatal(err)
	}
}

// A job with artifacts copies them when it ends, before its end is posted:
// the woken submitter sees what the copy found on the job itself.
func TestAJobCopiesItsArtifactsBeforeItsEnd(t *testing.T) {
	copied := make(chan []string, 1)
	svc, _, reporter, _ := newService(t, func(_ context.Context, _ string, _ io.Reader, out, stderr io.Writer, _ string, args ...string) error {
		for i, arg := range args {
			if strings.Contains(arg, "keep=()") {
				copied <- args[i+2:]
				fmt.Fprintln(stderr, "out.bin")
				return nil
			}
		}
		fmt.Fprintln(out, "built")
		return nil
	})
	if _, err := svc.Submit(t.Context(), Input{RoomID: "dyna", Command: []string{"make"}, Artifacts: []string{"out.bin"}}); err != nil {
		t.Fatal(err)
	}
	finished := <-reporter.finished
	svc.Stop()
	select {
	case got := <-copied:
		if len(got) != 2 || got[1] != "out.bin" {
			t.Fatalf("copy asked for %q, want the work folder and out.bin", got)
		}
	default:
		t.Fatal("the job's artifacts were never copied")
	}
	if finished.State != StatePassed || !strings.Contains(finished.Note, "artifacts not found: out.bin") {
		t.Fatalf("finished job = %s %q, want passed with the missing artifact named", finished.State, finished.Note)
	}
}
