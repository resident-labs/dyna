package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// RunAgentBindings are a run coordinator's calls for the leaf sessions that
// serve its agent() calls. sessions is called on each call, as for
// RunnerBindings. Each call is for the run's verified coordinator only.
func RunAgentBindings(sessions func() *subagent.Manager) []websvc.Binding {
	return []websvc.Binding{
		{Name: "StartRunAgent", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, in callspec.RunAgentStart) error {
			return startRunAgent(ctx, sessions(), in)
		}},
		{Name: "WaitRunAgent", Access: websvc.Read, Scope: websvc.ScopeRead, Local: true, Method: func(ctx context.Context, ref callspec.RunAgentRef) (callspec.RunAgentState, error) {
			if err := checkRunAgentCoordinator(ctx, ref.Run); err != nil {
				return callspec.RunAgentState{}, err
			}
			return waitRunAgent(ctx, sessions(), ref)
		}},
		{Name: "CloseRunAgents", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: func(ctx context.Context, run string) error {
			if err := checkRunAgentCoordinator(ctx, run); err != nil {
				return err
			}
			return sessions().CloseRunAgents(ctx, run)
		}},
	}
}

// checkRunAgentCoordinator admits the run's own coordinator. These calls carry
// no request ID: the session ID derived from the run and agent makes a repeat
// start safe, and a wait reads only.
func checkRunAgentCoordinator(ctx context.Context, run string) error {
	c, local := websvc.CallerFromContext(ctx)
	if !local || c.Kind != websvc.Worker || c.Agent != 0 || c.Session != "" || c.Run == "" || c.Run != run {
		return errors.New("run agents need the run's verified coordinator")
	}
	return runstore.ValidateRunID(run)
}

func startRunAgent(ctx context.Context, m *subagent.Manager, in callspec.RunAgentStart) error {
	if err := checkRunAgentCoordinator(ctx, in.Run); err != nil {
		return err
	}
	runDir := filepath.Join(runstore.RunsDir(), in.Run)
	for _, path := range []string{in.Journal, in.JournalRoot} {
		if path != "" && !within(runDir, path) {
			return fmt.Errorf("run agent journal %s is outside run %s", path, in.Run)
		}
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		return err
	}
	meta, err := runstore.LoadStored(ctx, db, in.Run)
	if err != nil {
		return err
	}
	_, err = m.StartRunAgent(ctx, subagent.StartOptions{
		Run:     &subagent.RunOwner{ID: in.Run, Agent: in.Agent, Journal: in.Journal, JournalRoot: in.JournalRoot, Profile: in.Inline},
		Profile: in.Profile, Dir: in.Dir, Task: in.Task, Schema: in.Schema, Deadline: in.Deadline, Room: meta.RoomID,
	})
	return err
}

// within reports whether path is dir or lies below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func waitRunAgent(ctx context.Context, m *subagent.Manager, ref callspec.RunAgentRef) (callspec.RunAgentState, error) {
	got, err := m.WaitRunAgent(ctx, ref.Run, ref.Agent, callspec.RunAgentWaitBound)
	if err != nil || !got.Done {
		return callspec.RunAgentState{}, err
	}
	state := callspec.RunAgentState{Done: true, Status: got.Turn.Status, Error: got.Turn.Error}
	if got.Turn.Result != nil {
		if state.Result, err = json.Marshal(got.Turn.Result); err != nil {
			return callspec.RunAgentState{}, err
		}
	}
	return state, nil
}
