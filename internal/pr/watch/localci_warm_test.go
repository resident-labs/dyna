package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// fakeWarmStore keeps the warmed heads a warm run records.
type fakeWarmStore struct {
	mu    sync.Mutex
	heads map[string]string
	saves int
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

// warmState is the tracked state a warm run reads its repository from.
func warmState(repo, room, base string) State {
	return State{Records: map[string]*state.Record{
		"change-1": {Host: "github.com", Repo: repo, RoomID: room, RepoPath: "/checkout/" + repo, Base: base, Watching: true},
	}}
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
	finished := make(chan struct{}, 4)
	s := warmService(t, []localci.Node{{Name: "hermeslab", Slots: 2, Enabled: true}}, store, func(_ context.Context, _ localci.Node, req localci.WarmRequest) error {
		started <- req
		<-release
		finished <- struct{}{}
		return nil
	})
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s.scheduleCacheWarm(context.Background(), warmState("owner/repo", "room_dyna", base))
	select {
	case req := <-started:
		if req.SHA != base || req.Job != "cache-warm" || req.Workflow != ".github/workflows/ci.yml" || req.RoomID != "room_dyna" {
			t.Fatalf("warm request = %+v", req)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no warm run started for a moved default branch")
	}
	// A second tick while the first run is in flight starts nothing.
	s.scheduleCacheWarm(context.Background(), warmState("owner/repo", "room_dyna", base))
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started, want one", got)
	}
	close(release)
	<-finished
	if store.saved() != 1 {
		t.Fatalf("%d warmed commits recorded, want one", store.saved())
	}
	// The commit is now warm, so the next tick starts nothing.
	s.scheduleCacheWarm(context.Background(), warmState("owner/repo", "room_dyna", base))
	if got := s.warmRuns().count(); got != 1 {
		t.Fatalf("%d warm runs started after the commit was warm, want one", got)
	}
}

// A machine with no free slot takes no warm run, and a repository that names no
// cache-warming job warms nothing.
func TestWarmWaitsForAFreeSlotAndNeedsAJob(t *testing.T) {
	store := newFakeWarmStore()
	runs := 0
	s := warmService(t, []localci.Node{{Name: "busy", Slots: 1, Enabled: true}}, store, func(context.Context, localci.Node, localci.WarmRequest) error {
		runs++
		return nil
	})
	// The one slot is taken by a pull request's check.
	s.nodeRuns().Lock()
	s.nodeRuns().runs = map[string]*registeredCIRun{"pr": {node: "busy", slots: 1}}
	s.nodeRuns().Unlock()
	s.scheduleCacheWarm(context.Background(), warmState("owner/repo", "room_dyna", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if got := s.warmRuns().count(); got != 0 || runs != 0 {
		t.Fatalf("%d warm runs started on a busy machine, want none", got)
	}
	// A repository without a warm job has nothing to run.
	plain := warmService(t, []localci.Node{{Name: "free", Slots: 4, Enabled: true}}, store, func(context.Context, localci.Node, localci.WarmRequest) error {
		runs++
		return nil
	})
	plain.WarmDefinition = func(context.Context, string, string) (*localci.Definition, error) {
		return localci.ParseDefinition([]byte("version: 1\ntrial: [go]\nchecks:\n  - id: build\n    image: golang:1.26.3\n    command: go build ./...\nsuites:\n  - id: go\n    checks: [build]\n"))
	}
	plain.scheduleCacheWarm(context.Background(), warmState("owner/repo", "room_dyna", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if got := plain.warmRuns().count(); got != 0 || runs != 0 {
		t.Fatalf("%d warm runs started without a warm job, want none", got)
	}
}
