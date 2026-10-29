package watch

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// fakeWarmStore keeps the warmed heads a warm run records.
type fakeWarmStore struct {
	mu      sync.Mutex
	heads   map[string]string
	saves   int
	savedCh chan struct{}
}

func newFakeWarmStore() *fakeWarmStore { return &fakeWarmStore{heads: map[string]string{}} }

func (f *fakeWarmStore) WarmedHead(_ context.Context, repo, node string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heads[repo+"\x00"+node], nil
}

func (f *fakeWarmStore) SaveWarm(_ context.Context, repo, node, head string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[repo+"\x00"+node] = head
	f.saves++
	if f.savedCh != nil {
		f.savedCh <- struct{}{}
	}
	return nil
}

func (f *fakeWarmStore) saved() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves
}

// warmNodes is the machine list a warm run picks from.
type warmNodes struct{ nodes []localci.Node }

func (w warmNodes) Nodes(context.Context) ([]localci.Node, error)  { return w.nodes, nil }
func (warmNodes) Mode(context.Context, string) (string, error)     { return "local", nil }
func (warmNodes) SaveResult(context.Context, localci.Result) error { return nil }
func (warmNodes) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, nil
}
func (warmNodes) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return localci.HostedRuns{}, nil
}
func (warmNodes) SaveHostedRuns(context.Context, string, string, localci.HostedRuns) error {
	return nil
}

// warmState is the tracked state a warm run reads its repository from: the
// change names the room and the checkout, the facts name the default branch and
// its head.
func warmState(t *testing.T, repo, room, base string) State {
	t.Helper()
	return warmStateIn(t, repo, []roomCheckout{{room: room, path: t.TempDir()}}, base)
}

// roomCheckout is one room's change of a repository and the checkout it keeps.
type roomCheckout struct {
	room, path, lookup string
}

// warmStateIn builds tracked state with one change per room. A change carries no
// worktree of its own when lookup is set, which is how a merged change looks
// after its session's worktree was removed.
func warmStateIn(t *testing.T, repo string, rooms []roomCheckout, base string) State {
	t.Helper()
	records := map[string]*state.Record{}
	for i, rc := range rooms {
		record := &state.Record{Host: "github.com", Repo: repo, RoomID: rc.room, Base: "old-base", Watching: false}
		if rc.lookup != "" {
			record.LookupDir = rc.lookup
		} else {
			record.RepoPath = rc.path
		}
		records[fmt.Sprintf("change-%d", i)] = record
	}
	return State{
		Records: records,
		RepositoryFacts: map[string]state.RepositoryFacts{
			"github.com/" + repo: {DefaultBranch: "main", Head: base},
		},
	}
}

func warmService(t *testing.T, nodes []localci.Node, store *fakeWarmStore, run func(context.Context, localci.Node, localci.WarmRequest) error) *Service {
	return &Service{
		Store:           &Store{Root: t.TempDir()},
		LocalCIRegistry: warmNodes{nodes: nodes},
		WarmStore:       store,
		WarmDefinition: func(_ context.Context, _, _ string) (*localci.Definition, error) {
			return localci.ParseDefinition([]byte("version: 1\ntrial: [go]\nwarm: { workflow: .github/workflows/ci.yml, name: cache-warm }\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n"))
		},
		WarmRun: run,
	}
}

// A machine warms the repository once per default branch commit, so a second
// tick on the same commit starts nothing and a new commit starts again.
func TestWarmRunsOncePerCommitAndMachine(t *testing.T) {
	store := newFakeWarmStore()
	release := make(chan struct{})
	started := make(chan localci.WarmRequest, 4)
	// The store signals after it saved, so the assertion below reads a finished
	// run rather than one that has not recorded its commit yet.
	saved := make(chan struct{}, 4)
	store.savedCh = saved
	s := warmService(t, []localci.Node{{Name: "hermeslab", Enabled: true}}, store, func(_ context.Context, _ localci.Node, req localci.WarmRequest) error {
		started <- req
		<-release
		return nil
	})
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s.scheduleCacheWarm(context.Background(), warmState(t, "owner/repo", "room_dyna", base))
	// Admission is synchronous, so a missing run fails here without waiting.
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started for a moved default branch, want one", got)
	}
	req := <-started
	if req.SHA != base || req.Job != "cache-warm" || req.Workflow != ".github/workflows/ci.yml" || req.RoomID != "room_dyna" {
		t.Fatalf("warm request = %+v", req)
	}
	// A second tick while the first run is in flight starts nothing.
	s.scheduleCacheWarm(context.Background(), warmState(t, "owner/repo", "room_dyna", base))
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started, want one", got)
	}
	close(release)
	<-saved
	if store.saved() != 1 {
		t.Fatalf("%d warmed commits recorded, want one", store.saved())
	}
	// The commit is now warm, so the next tick starts nothing.
	s.scheduleCacheWarm(context.Background(), warmState(t, "owner/repo", "room_dyna", base))
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started after the commit was warm, want one", got)
	}
}

// A machine with no room in its node queue takes no warm run, and a repository
// that names no cache-warming job warms nothing.
func TestWarmWaitsForRoomAndNeedsAJob(t *testing.T) {
	store := newFakeWarmStore()
	runs := 0
	s := warmService(t, []localci.Node{{Name: "busy", Enabled: true, CPUs: 2}}, store, func(context.Context, localci.Node, localci.WarmRequest) error {
		runs++
		return nil
	})
	// The machine's whole queue is held by other work.
	queue := jobs.Admission{DataDir: filepath.Dir(s.Store.Root)}
	if _, err := queue.Hold(t.Context(), jobs.Demand{Node: "busy", ID: "held", CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	s.scheduleCacheWarm(context.Background(), warmState(t, "owner/repo", "room_dyna", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if got := s.warmRuns().count(); got != 0 || runs != 0 {
		t.Fatalf("%d warm runs started on a busy machine, want none", got)
	}
	// A repository without a warm job has nothing to run.
	plain := warmService(t, []localci.Node{{Name: "free", Enabled: true}}, store, func(context.Context, localci.Node, localci.WarmRequest) error {
		runs++
		return nil
	})
	plain.WarmDefinition = func(context.Context, string, string) (*localci.Definition, error) {
		return localci.ParseDefinition([]byte("version: 1\ntrial: [go]\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n"))
	}
	plain.scheduleCacheWarm(context.Background(), warmState(t, "owner/repo", "room_dyna", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if got := plain.warmRuns().count(); got != 0 || runs != 0 {
		t.Fatalf("%d warm runs started without a warm job, want none", got)
	}
}

// A repository can have changes in two rooms with machines that serve only one
// room each, and every machine warms through the room it serves.
func TestWarmTakesTheRoomEachMachineServes(t *testing.T) {
	store := newFakeWarmStore()
	requests := make(chan localci.WarmRequest, 4)
	s := warmService(t, []localci.Node{
		{Name: "hermeslab", Enabled: true, Rooms: []string{"room_one"}},
		{Name: "pve", Enabled: true, Rooms: []string{"room_two"}},
	}, store, func(_ context.Context, node localci.Node, req localci.WarmRequest) error {
		requests <- req
		return nil
	})
	base := strings.Repeat("a", 40)
	state := warmStateIn(t, "owner/repo", []roomCheckout{{room: "room_one", path: t.TempDir()}, {room: "room_two", path: t.TempDir()}}, base)
	s.scheduleCacheWarm(context.Background(), state)
	if got := s.warmRuns().count(); got != 2 {
		t.Fatalf("%d warm runs started, want one per room", got)
	}
	rooms := map[string]string{}
	for range 2 {
		req := <-requests
		rooms[req.RepoDir] = req.RoomID
	}
	for _, room := range rooms {
		if room != "room_one" && room != "room_two" {
			t.Fatalf("warm run took room %q", room)
		}
	}
}

// matchingRepoDir makes a checkout whose remote names the repository, which is
// what a fallback directory has to prove before Dyna reads it.
func matchingRepoDir(t *testing.T, repo string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "https://github.com/" + repo},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// A merged change keeps no worktree of its own, so the warm run reads the
// definition from the directory that is still there instead of skipping.
func TestWarmUsesALiveCheckoutOfAMergedChange(t *testing.T) {
	store := newFakeWarmStore()
	requests := make(chan localci.WarmRequest, 2)
	s := warmService(t, []localci.Node{{Name: "hermeslab", Enabled: true}}, store, func(_ context.Context, _ localci.Node, req localci.WarmRequest) error {
		requests <- req
		return nil
	})
	live := matchingRepoDir(t, "owner/repo")
	state := warmStateIn(t, "owner/repo", []roomCheckout{{room: "room_dyna", lookup: live}}, strings.Repeat("a", 40))
	s.scheduleCacheWarm(context.Background(), state)
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started from the live checkout, want one", got)
	}
	if req := <-requests; req.RepoDir != live {
		t.Fatalf("warm run works in %q, want the live checkout %q", req.RepoDir, live)
	}
}
