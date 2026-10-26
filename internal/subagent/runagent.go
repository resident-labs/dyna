package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
)

// RunAgentID is the session ID of agent number agent in run. It is derived, so
// a coordinator that restarts finds the session its earlier call started
// instead of starting a second one. The date and time are the run's own (a run
// ID is wf_<date>-<time>-<hex>), so the agents of a run sort with it; the
// suffix is 16 hex digits of a hash of the run and agent.
func RunAgentID(run string, agent int) string {
	stamp := "20000101-000000"
	if rest, ok := strings.CutPrefix(run, "wf_"); ok && len(rest) >= len(stamp) {
		if _, err := time.Parse("20060102-150405", rest[:len(stamp)]); err == nil {
			stamp = rest[:len(stamp)]
		}
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/agent:%d", run, agent))
	return runstore.SessionDirPrefix + stamp + "-" + hex.EncodeToString(sum[:8])
}

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

// CloseRunAgents closes every open session that serves an agent call of run.
func (m *Manager) CloseRunAgents(ctx context.Context, run string) error {
	infos, err := m.store().ListSnapshot(false)
	if err != nil {
		return err
	}
	var errs []error
	for _, info := range infos {
		if info.Run == nil || info.Run.ID != run || info.Status == StatusClosed {
			continue
		}
		if _, err := m.closeRunAgent(ctx, info.ID); err != nil && !errors.Is(err, ErrClosed) {
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
	return m.CloseForEngine(ctx, id, "run agent of "+meta.Run.ID, CloseOptions{Force: true})
}
