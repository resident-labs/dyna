package subagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runprocs"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shells"
)

// RunAgentID is the session ID of agent number agent in run. It is derived, so
// a coordinator that restarts finds the session its earlier call started
// instead of starting a second one.
func RunAgentID(run string, agent int) string { return runstore.AgentSessionID(run, agent) }

// StartRunAgent starts the leaf session that serves one agent() call of
// opts.Run, or returns the session an earlier call already started. It starts
// no turn twice: a session that exists is returned as it is.
func (m *Manager) StartRunAgent(ctx context.Context, opts StartOptions) (Info, error) {
	if opts.Run == nil || opts.Run.ID == "" || opts.Run.Agent < 0 {
		return Info{}, errors.New("a run agent needs its run and agent number")
	}
	if opts.Dir == "" {
		return Info{}, errors.New("a run agent needs its working directory")
	}
	opts.ID = RunAgentID(opts.Run.ID, opts.Run.Agent)
	// A close of this agent (or its run) that came first wins: the start may
	// have outlived its caller's request, and a session must not appear after
	// the close that was meant to stop it.
	done, ok := m.gates().begin(opts.Run.ID)
	if !ok {
		return Info{}, fmt.Errorf("run agent %d of %s is closed", opts.Run.Agent, opts.Run.ID)
	}
	defer done()
	release, err := m.gates().acquire(ctx, opts.ID)
	if err != nil {
		return Info{}, err
	}
	defer release()
	if m.gates().closed(opts.ID, opts.Run.ID) || runEnded(opts.Run.ID) {
		return Info{}, fmt.Errorf("run agent %d of %s is closed", opts.Run.Agent, opts.Run.ID)
	}
	if m.runAgentStartHook != nil {
		m.runAgentStartHook(opts.ID)
	}
	if meta, err := m.store().ReadMeta(opts.ID); err == nil {
		if meta.Run == nil || meta.Run.ID != opts.Run.ID || meta.Run.Agent != opts.Run.Agent {
			return Info{}, fmt.Errorf("session %s belongs to another run agent", opts.ID)
		}
		return m.store().Info(opts.ID)
	}
	info, _, err := m.start(ctx, opts, "", "")
	return info, err
}

// RunAgentWait is the state of a run agent's first turn.
type RunAgentWait struct {
	Done bool
	Turn Turn
}

// WaitRunAgent waits up to bound for the first turn of a run agent to end. A
// turn still running at the bound returns Done false, and the caller asks
// again; a bound keeps one engine call short, and the next call also covers
// an engine restart. Once the turn is done the session closes: it served its
// one call.
func (m *Manager) WaitRunAgent(ctx context.Context, run string, agent int, bound time.Duration) (RunAgentWait, error) {
	id := RunAgentID(run, agent)
	// An earlier call may have closed the session before its caller read the
	// result, so a closed session still answers from its stored turn.
	if meta, err := m.store().ReadMeta(id); err == nil && !meta.Closed.IsZero() {
		turn, err := m.store().ReadTurn(id, 1)
		return RunAgentWait{Done: err == nil, Turn: turn}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	_, turn, err := m.Wait(waitCtx, id, 1)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return RunAgentWait{Turn: turn}, nil
		}
		return RunAgentWait{}, err
	}
	if _, err := m.closeRunAgent(ctx, id); err != nil && !errors.Is(err, ErrClosed) {
		return RunAgentWait{}, err
	}
	return RunAgentWait{Done: true, Turn: turn}, nil
}

// CloseRunAgent closes the session of one agent call of run, and stops its turn
// if one is running. A nested script that ends with an agent still running
// uses it before its concurrency slots are released.
//
// It waits for a start of the same agent that is still running, then closes
// what that start made. When no session exists yet, it records the close so a
// later start of the agent is refused.
func (m *Manager) CloseRunAgent(ctx context.Context, run string, agent int) error {
	id := RunAgentID(run, agent)
	release, err := m.gates().acquire(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	m.gates().markClosed(id)
	if _, err := m.closeRunAgent(ctx, id); err != nil && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// CloseRunAgents closes every open session that serves an agent call of run.
func (m *Manager) CloseRunAgents(ctx context.Context, run string) error {
	// No start of this run begins after this, and the ones in flight finish
	// first, so the snapshot below holds every session the run made.
	if err := m.gates().closeRun(ctx, run); err != nil {
		return err
	}
	infos, err := m.store().ListSnapshot(false)
	if err != nil {
		return err
	}
	var errs []error
	for _, info := range infos {
		if info.Run == nil || info.Run.ID != run || info.Status == StatusClosed {
			continue
		}
		// An agent whose start is still running closes after that start ends.
		release, err := m.gates().acquire(ctx, info.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, err = m.closeRunAgent(ctx, info.ID)
		release()
		if err != nil && !errors.Is(err, ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CloseEndedRunAgents closes the open run agents whose run is over. The engine
// calls it when a run's state changes.
func (m *Manager) CloseEndedRunAgents(ctx context.Context) error {
	sessions, err := m.store().listMeta(false, true)
	if err != nil {
		return err
	}
	return m.closeEndedRunAgents(ctx, sessions)
}

// closeEndedRunAgents closes the open run agents whose run is over. The
// engine's session pass calls it, so a run that ended any way (finished,
// failed, canceled, or a coordinator that died) leaves no agent session open.
func (m *Manager) closeEndedRunAgents(ctx context.Context, sessions []Meta) error {
	ended := map[string]bool{}
	openRuns := map[string]bool{}
	var errs []error
	for _, meta := range sessions {
		if meta.Run == nil || !meta.Closed.IsZero() {
			continue
		}
		over, known := ended[meta.Run.ID]
		if !known {
			over = runEnded(meta.Run.ID)
			ended[meta.Run.ID] = over
		}
		if !over {
			continue
		}
		if _, err := m.closeRunAgent(ctx, meta.ID); err != nil && !errors.Is(err, ErrClosed) {
			errs = append(errs, err)
		}
		openRuns[meta.Run.ID] = true
	}
	// Every ended run, however it ended (ok, error, interrupted, canceled, lost
	// coordinator) and whether or not its agents closed earlier.
	if err := m.endEndedRuns(openRuns); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// runEnded reports a run that finished, or whose coordinator process is gone.
// A run that cannot be read is not known to be over.
func runEnded(run string) bool {
	meta, err := runstore.ReadMeta(run)
	if err != nil {
		return false
	}
	return runstore.IsFinishedStatus(meta.Status) || meta.Status == "running" && !runstore.Alive(meta)
}

// closeRunAgent closes the session of a run agent, and stops its turn if one is
// running. Callers have verified that the session belongs to the run: the
// coordinator by the derived ID, the engine by the stored run. The coordinator
// is a worker, which the close and stop rules refuse, so the close acts as the
// engine does for the session's room (as CloseForEngine does for maintenance).
func (m *Manager) closeRunAgent(ctx context.Context, id string) (Info, error) {
	meta, err := m.store().ReadMeta(id)
	if err != nil {
		return Info{}, err
	}
	if meta.Run == nil {
		return Info{}, fmt.Errorf("session %s is not a run agent", id)
	}
	ctx = authority.WithActor(ctx, authority.EngineRoomActor(meta.RoomID))
	info, err := m.CloseForEngine(ctx, id, "run agent of "+meta.Run.ID, CloseOptions{Force: true})
	if err != nil || !meta.Closed.IsZero() {
		return info, err
	}
	// The agent is done, so its processes end with it; siblings keep running.
	// A failed kill never costs the agent its result: it goes to the run's log,
	// and the run's end cleanup tries again.
	if err := m.runShells().EndSessionProcesses(id, m.killFunc()); err != nil {
		_ = runstore.AppendSystemLog(meta.Run.ID, fmt.Sprintf("could not end the processes of agent %d: %v", meta.Run.Agent, err))
	}
	return info, nil
}

// processCleanupWindow limits the first pass to runs that ended recently:
// runs that ended before the mark existed carry none, and ending processes
// by a stamp that old would only scan /proc for nothing.
const processCleanupWindow = 24 * time.Hour

// endEndedRuns ends what the workers of each ended run left running, as a
// cancel does, and marks the run (runstore.Meta.ProcessesEnded). The marks live
// in the run store, so an engine restart forgets nothing, and a failed cleanup
// stays unmarked for the next pass. extra holds ended runs the caller found
// through their open agents; the rest come from the store. It never takes the
// run's own group (leader 0), and the coordinator carries no run stamp, so a
// coordinator still inside its finish call is not touched. A process that lost
// DYNA_SESSION keeps DYNA_WORKER_RUN.
func (m *Manager) endEndedRuns(extra map[string]bool) error {
	runs := map[string]bool{}
	for run := range extra {
		runs[run] = true
	}
	var errs []error
	metas, err := runstore.EndedWithoutCleanup(time.Now().Add(-processCleanupWindow))
	if err != nil {
		errs = append(errs, err)
	}
	for _, meta := range metas {
		runs[meta.ID] = true
	}
	for run := range runs {
		meta, err := runstore.ReadMeta(run)
		if err != nil || meta.ProcessesEnded != nil {
			continue
		}
		if err := m.runShells().EndRunProcesses(run, 0, m.killFunc()); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", run, err))
			continue
		}
		// A run that still reads running (lost coordinator) takes no mark yet.
		if err := runstore.MarkProcessesEnded(run); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) runShells() *shells.Store {
	return &shells.Store{Root: filepath.Join(filepath.Dir(m.store().root()), "shells")}
}

func (m *Manager) killFunc() shells.KillFunc {
	if m.killProcs != nil {
		return m.killProcs
	}
	return runprocs.KillStamped
}

// runAgentGateSets holds the gates of each session store, so one store has one
// set of gates however its Manager is built.
var runAgentGateSets sync.Map

func (m *Manager) gates() *runAgentGates {
	set, _ := runAgentGateSets.LoadOrStore(m.store().root(), &runAgentGates{})
	return set.(*runAgentGates)
}

// runAgentGates serializes the start and the close of each run agent, and
// remembers a close that found no session, so the two cannot cross.
type runAgentGates struct {
	mu         sync.Mutex
	slots      map[string]chan struct{}
	closedID   map[string]bool
	closedRuns map[string]bool
	inflight   map[string]*sync.WaitGroup
}

// begin registers a start of run, or reports that the run's agents are closed.
// The caller calls done when the start has ended.
func (g *runAgentGates) begin(run string) (done func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closedRuns[run] {
		return nil, false
	}
	if g.inflight == nil {
		g.inflight = map[string]*sync.WaitGroup{}
	}
	wg := g.inflight[run]
	if wg == nil {
		wg = &sync.WaitGroup{}
		g.inflight[run] = wg
	}
	wg.Add(1)
	return wg.Done, true
}

// closeRun refuses later starts of run and waits for the ones in flight.
func (g *runAgentGates) closeRun(ctx context.Context, run string) error {
	g.markRunClosed(run)
	g.mu.Lock()
	wg := g.inflight[run]
	g.mu.Unlock()
	if wg == nil {
		return nil
	}
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// acquire waits for id's gate. It gives up when ctx ends.
func (g *runAgentGates) acquire(ctx context.Context, id string) (func(), error) {
	g.mu.Lock()
	if g.slots == nil {
		g.slots = map[string]chan struct{}{}
	}
	slot := g.slots[id]
	if slot == nil {
		slot = make(chan struct{}, 1)
		g.slots[id] = slot
	}
	g.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (g *runAgentGates) markClosed(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closedID == nil {
		g.closedID = map[string]bool{}
	}
	g.closedID[id] = true
}

func (g *runAgentGates) markRunClosed(run string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closedRuns == nil {
		g.closedRuns = map[string]bool{}
	}
	g.closedRuns[run] = true
}

func (g *runAgentGates) closed(id, run string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closedID[id] || g.closedRuns[run]
}
