package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// runningCIRegistry offers one 16-slot machine and no saved result.
type runningCIRegistry struct{ hostedCIRegistry }

func (r *runningCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Slots: 16, Enabled: true}}, nil
}
func (r *runningCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *runningCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}

// A run in flight names its machine, its slots and its PR, and it leaves the
// snapshot when it ends.
func TestRunningCIReportsAMachineRunUntilItEnds(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", name)
		git("commit", "--quiet", "-m", name)
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet")
	git("config", "user.email", "ci@example.com")
	git("config", "user.name", "CI")
	base := commit("README", "before\n")
	head := commit("main.go", "package main\n")

	root := filepath.Join(t.TempDir(), "prs")
	record := &Record{Key: "pr", Kind: "github", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 17, Head: head, Base: base, Branch: "feature", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
	saved := State{Version: 1, Records: map[string]*Record{record.Key: record}}
	if err := state.Write(root, saved); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1000, 0)
	s := &Service{Store: &Store{Root: root}, LocalCIRegistry: &runningCIRegistry{}, Now: func() time.Time { return started }}
	s.GH.run = func(context.Context, *exec.Cmd) error { return nil }
	running := make(chan string, 1)
	s.RunNodeCI = func(ctx context.Context, node localci.Node, _ localci.Request) (localci.Result, error) {
		running <- node.Name
		<-ctx.Done()
		return localci.Result{}, ctx.Err()
	}
	changed := make(chan struct{}, 8)
	s.RunningCIChanged = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}

	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-running:
		if name != "hermes" {
			t.Fatalf("run started on %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no local CI run started")
	}
	list := s.RunningCI()
	if len(list) != 1 {
		t.Fatalf("running CI = %+v", list)
	}
	got := list[0]
	if got.Machine != "hermes" || got.Slots != registeredCIRunSlots || got.Repo != "owner/repo" || got.Number != 17 || got.Head != head[:7] || got.Trial || !got.Started.Equal(started) {
		t.Fatalf("running CI = %+v", got)
	}
	if suites := strings.Join(got.Suites, ","); suites != "go,cross-build" {
		t.Fatalf("running CI suites = %q", suites)
	}

	// The PR is no longer tracked, so the run is cancelled and must leave the
	// snapshot for clients.
	if err := state.Write(root, State{Version: 1, Records: map[string]*Record{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleRegisteredCI(context.Background(), State{Version: 1}); err != nil {
		t.Fatal(err)
	}
	for waiting := true; waiting; {
		select {
		case <-changed:
			waiting = len(s.RunningCI()) != 0
		case <-time.After(5 * time.Second):
			t.Fatalf("finished run stayed in the snapshot: %+v", s.RunningCI())
		}
	}
}
