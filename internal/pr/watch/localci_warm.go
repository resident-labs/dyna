package watch

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/localci"
)

// warmTimeout bounds one warm run. A warm run compiles what the job checks
// later restore, so it gets more room than a single check.
const warmTimeout = 30 * time.Minute

// warmStore remembers the default branch commit each machine last warmed, so a
// commit warms a machine once.
type warmStore interface {
	WarmedHead(ctx context.Context, repo, node string) (string, error)
	SaveWarm(ctx context.Context, repo, node, head string, at time.Time) error
}

// warmRuns is the set of warm runs in flight, keyed by repository and machine.
// The engine cancels them when it stops, like every other local CI run.
type warmRuns struct {
	sync.Mutex
	closed atomic.Bool
	runs   map[string]context.CancelFunc
	// started counts the warm runs this process admitted, including the ones
	// that have already ended.
	started int
}

func (w *warmRuns) start(key string, cancel context.CancelFunc) bool {
	w.Lock()
	defer w.Unlock()
	if w.closed.Load() || w.runs[key] != nil {
		return false
	}
	w.runs[key] = cancel
	w.started++
	return true
}

// count reports how many warm runs this process admitted.
func (w *warmRuns) count() int {
	w.Lock()
	defer w.Unlock()
	return w.started
}

func (w *warmRuns) done(key string) {
	w.Lock()
	defer w.Unlock()
	delete(w.runs, key)
}

// cancelAll ends every warm run in flight and refuses new ones.
func (w *warmRuns) cancelAll() {
	w.Lock()
	defer w.Unlock()
	w.closed.Store(true)
	for key, cancel := range w.runs {
		cancel()
		delete(w.runs, key)
	}
}

func (s *Service) warmRuns() *warmRuns {
	localCIInit.Lock()
	defer localCIInit.Unlock()
	if s.registeredWarms == nil {
		s.registeredWarms = &warmRuns{runs: map[string]context.CancelFunc{}}
	}
	return s.registeredWarms
}

// warmDefinition reads the definition at the default branch commit, which names
// the job that fills the act cache.
func (s *Service) warmDefinitionAt(ctx context.Context, repo, base string) (*localci.Definition, error) {
	if s.WarmDefinition != nil {
		return s.WarmDefinition(ctx, repo, base)
	}
	return localci.LoadDefinition(ctx, repo, base)
}

// warmStoreFor returns the store that remembers warmed commits.
func (s *Service) warmStoreFor() warmStore {
	if s.WarmStore != nil {
		return s.WarmStore
	}
	return localci.Store{DataDir: filepath.Dir(s.Store.Root)}
}

// runWarm runs one warm run on a machine, or the injected one in a test. It
// reports nil only when the run succeeded.
func (s *Service) runWarm(ctx context.Context, node localci.Node, req localci.WarmRequest) error {
	if s.WarmRun != nil {
		return s.WarmRun(ctx, node, req)
	}
	result, err := (localci.Runner{DataDir: filepath.Dir(s.Store.Root), Node: &node}).Warm(ctx, req)
	if err != nil {
		return err
	}
	if result.Status != localci.OutcomePassed {
		return fmt.Errorf("warm run ended %s: %s", result.Status, result.Error)
	}
	return nil
}

// scheduleCacheWarm starts a warm run for every machine that serves a
// repository whose default branch moved. The run fills the act cache the job
// checks restore, so their first check does not compile the whole tree.
//
// The default branch and its head come from the repository facts, which the
// head refresh writes for every repository Dyna knows, so a repository whose
// changes have all merged still warms and a stacked change never warms the
// branch it is stacked on. A machine takes the room it serves, because one
// repository can have changes in rooms with different machines.
func (s *Service) scheduleCacheWarm(ctx context.Context, st State) {
	if s.Store == nil {
		return
	}
	nodes, err := s.registeredCINodes(ctx)
	if err != nil || len(nodes) == 0 {
		log.Printf("warm local CI caches: %v", err)
		return
	}
	store := s.warmStoreFor()
	if store == nil {
		return
	}
	active := s.activeCISlots()
	repos := make([]string, 0, len(st.RepositoryFacts))
	for repo := range st.RepositoryFacts {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		facts := st.RepositoryFacts[repo]
		base, branch := facts.Head, facts.DefaultBranch
		if base == "" {
			continue
		}
		if branch == "" {
			branch = "main"
		}
		byRoom := s.recordsByRoom(st, repo)
		if len(byRoom) == 0 {
			continue
		}
		warm, ok := s.warmJobAt(ctx, st, repo, base, byRoom)
		if !ok {
			continue
		}
		for i := range nodes {
			node := nodes[i]
			// A warm run takes a slot, so it waits for a machine with a free
			// one: a pull request's checks come first.
			if node.LowDisk || node.Slots-active[node.Name] < 1 {
				continue
			}
			warmed, err := store.WarmedHead(ctx, repo, node.Name)
			if err != nil || warmed == base {
				continue
			}
			// The machine warms once per commit, for the first room it serves.
			for _, room := range sortedRooms(byRoom) {
				if !node.AllowsRoom(room) {
					continue
				}
				dir, err := s.checkoutDir(ctx, byRoom[room])
				if err != nil {
					continue
				}
				s.startWarm(ctx, store, node, localci.WarmRequest{
					Repo: repo, RepoDir: dir, RoomID: room, SHA: base,
					Workflow: warm.Workflow, Job: warm.Name, Branch: branch, Timeout: warmTimeout,
				})
				break
			}
		}
	}
}

// recordsByRoom keeps one change per room for a repository, so a machine takes
// the room it serves and the checkout that room's change still has.
func (s *Service) recordsByRoom(st State, repo string) map[string]Record {
	byRoom := map[string]Record{}
	for _, record := range st.Records {
		if record == nil || record.RoomID == "" || defaultRepo(*record) != repo {
			continue
		}
		if _, ok := byRoom[record.RoomID]; !ok {
			byRoom[record.RoomID] = *record
		}
	}
	return byRoom
}

func sortedRooms(byRoom map[string]Record) []string {
	rooms := make([]string, 0, len(byRoom))
	for room := range byRoom {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	return rooms
}

// warmJobAt reads the cache-warming job from the definition at base, through a
// checkout that is still on disk: a change keeps the worktree of the session
// that tracked it, and that worktree goes when the session closes.
func (s *Service) warmJobAt(ctx context.Context, st State, repo, base string, byRoom map[string]Record) (localci.JobRef, bool) {
	for _, room := range sortedRooms(byRoom) {
		dir, err := s.checkoutDir(ctx, byRoom[room])
		if err != nil {
			continue
		}
		definition, err := s.warmDefinitionAt(ctx, dir, base)
		if err != nil {
			continue
		}
		if warm, ok := definition.Warm(); ok {
			return warm, true
		}
		return localci.JobRef{}, false
	}
	return localci.JobRef{}, false
}

// activeCISlots counts the slots the runs in flight hold, under the run set's
// own lock: a run that finishes removes its entry while a tick reads them.
func (s *Service) activeCISlots() map[string]int {
	runs := s.nodeRuns()
	runs.Lock()
	defer runs.Unlock()
	return activeRegisteredCISlots(runs)
}

// startWarm runs one warm run in the background and records the commit it
// warmed, so the same commit does not start another.
func (s *Service) startWarm(ctx context.Context, store warmStore, node localci.Node, req localci.WarmRequest) {
	key := req.Repo + "\x00" + node.Name
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if !s.warmRuns().start(key, cancel) {
		cancel()
		return
	}
	go func() {
		defer func() {
			cancel()
			s.warmRuns().done(key)
		}()
		if err := s.runWarm(runCtx, node, req); err != nil {
			log.Printf("warm local CI caches on %s: %v", node.Name, err)
			return
		}
		if err := store.SaveWarm(runCtx, req.Repo, node.Name, req.SHA, s.now()); err != nil {
			log.Printf("record warm run on %s: %v", node.Name, err)
		}
	}()
}
