package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/runstore"
)

// RunAgentID is the session ID of agent number agent in run. It is derived, so
// a coordinator that restarts finds the session its earlier call started
// instead of starting a second one. The date and time parts are fixed because
// the ID shape needs them; Created holds the real start time.
func RunAgentID(run string, agent int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/agent:%d", run, agent))
	return "ses_20000101-000000-" + hex.EncodeToString(sum[:4])
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
	if _, err := m.CloseWithOptions(ctx, id, CloseOptions{Force: true}); err != nil && !errors.Is(err, ErrClosed) {
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
		if _, err := m.CloseWithOptions(ctx, info.ID, CloseOptions{Force: true}); err != nil && !errors.Is(err, ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
		if _, err := m.CloseWithOptions(ctx, meta.ID, CloseOptions{Force: true}); err != nil && !errors.Is(err, ErrClosed) {
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
