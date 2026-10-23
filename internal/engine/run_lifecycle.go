package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

// RunLifecycleBindings exposes the production coordinator handlers to the
// engine and isolated workflow fixtures. Each write requires its own run.
func RunLifecycleBindings() []websvc.Binding {
	return []websvc.Binding{
		{Name: "FinishRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: finishRun},
		{Name: "AddRunDirectories", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: addRunDirectories},
	}
}

// checkRunCoordinator also runs before returning a cached receipt. Neither
// environment claims nor the report-only caller fallback authorize a write.
func checkRunCoordinator(ctx context.Context, run string) error {
	c, local := websvc.CallerFromContext(ctx)
	if !local || c.Kind != websvc.Worker || c.Agent != 0 || c.Session != "" || c.Run == "" || c.Run != run {
		return errors.New("run lifecycle needs the run's verified coordinator")
	}
	if callspec.RequestOf(ctx) == "" {
		return errors.New("run lifecycle needs a request ID")
	}
	return callspec.CheckRunnerRequestID(run, callspec.RequestOf(ctx))
}

func addRunDirectories(ctx context.Context, in callspec.RunDirectories) error {
	if err := checkRunCoordinator(ctx, in.Run); err != nil {
		return err
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		return err
	}
	return writeRunDirectories(ctx, state.For(db), in)
}

// writeRunDirectories commits the directory union and its request receipt in
// one transaction. Recovery supplies Worker(run,0) without a socket peer.
func writeRunDirectories(ctx context.Context, w *state.Writer, in callspec.RunDirectories) error {
	actor, ok := authority.ContextActor(ctx)
	if !ok || actor.Role != authority.Worker || actor.Run != in.Run || actor.Agent != 0 || actor.Session != "" {
		return errors.New("run lifecycle needs its coordinator actor")
	}
	id := callspec.RequestOf(ctx)
	if id == "" {
		return errors.New("run lifecycle needs a request ID")
	}
	if err := callspec.CheckRunnerRequestID(in.Run, id); err != nil {
		return err
	}
	if err := runstore.ValidateRunID(in.Run); err != nil {
		return err
	}
	var room string
	err := w.Read(ctx, func(q state.Reader) error {
		meta, err := runstore.LoadStored(ctx, q, in.Run)
		room = meta.RoomID
		return err
	})
	if err != nil {
		return err
	}
	event := hub.Event{Topic: "runs", Room: room, Key: in.Run}
	return w.Write(ctx, event, func(conn *sql.Conn) error {
		meta, err := runstore.LoadStored(ctx, conn, in.Run)
		if err != nil {
			return err
		}
		if meta.RoomID != room {
			return errors.New("run identity changed before its lifecycle write")
		}
		if err := runstore.AddDirectoriesTx(ctx, conn, in.Run, in.Directories); err != nil {
			return err
		}
		return w.FinishRequestIn(ctx, conn, "AddRunDirectories", []byte("null"))
	})
}

// runDirectoriesRecoveryBinding replays a spooled directory write under the
// run's coordinator actor.
func runDirectoriesRecoveryBinding(w *state.Writer) websvc.Binding {
	return websvc.Binding{Name: "AddRunDirectories", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, in callspec.RunDirectories) error {
		return writeRunDirectories(ctx, w, in)
	}}
}

func finishRun(ctx context.Context, in callspec.RunFinish) (runstore.Meta, error) {
	if err := checkRunCoordinator(ctx, in.Run); err != nil {
		return runstore.Meta{}, err
	}
	db, err := database.Open(store.DataDir())
	if err != nil {
		return runstore.Meta{}, err
	}
	return writeRunFinish(ctx, state.For(db), in)
}

// runLifecycleEvent admits only the authenticated actor. Recovery supplies
// Worker(run,0) without inventing a verified socket peer.
func runLifecycleEvent(ctx context.Context, w *state.Writer, run string) (hub.Event, error) {
	if err := runstore.ValidateRunID(run); err != nil {
		return hub.Event{}, err
	}
	actor, ok := authority.ContextActor(ctx)
	if !ok || actor.Role != authority.Worker || actor.Run != run || actor.Agent != 0 || actor.Session != "" {
		return hub.Event{}, errors.New("run lifecycle needs its coordinator actor")
	}
	if callspec.RequestOf(ctx) == "" {
		return hub.Event{}, errors.New("run lifecycle needs a request ID")
	}
	if err := callspec.CheckRunnerRequestID(run, callspec.RequestOf(ctx)); err != nil {
		return hub.Event{}, err
	}
	var meta runstore.Meta
	err := w.Read(ctx, func(q state.Reader) error {
		var err error
		meta, err = runstore.LoadStored(ctx, q, run)
		if err == nil && meta.ID != run {
			err = errors.New("stored run identity differs from the coordinator")
		}
		return err
	})
	return hub.Event{Topic: "runs", Room: meta.RoomID, Key: run}, err
}

// Recheck the event's canonical identity under the writer transaction.
func checkRunEvent(ctx context.Context, conn *sql.Conn, event hub.Event) error {
	meta, err := runstore.LoadStored(ctx, conn, event.Key)
	if err != nil {
		return err
	}
	if meta.ID != event.Key || meta.RoomID != event.Room {
		return errors.New("run identity changed before its lifecycle write")
	}
	return nil
}

func writeRunFinish(ctx context.Context, w *state.Writer, in callspec.RunFinish) (runstore.Meta, error) {
	event, err := runLifecycleEvent(ctx, w, in.Run)
	if err != nil {
		return runstore.Meta{}, err
	}
	var meta runstore.Meta
	err = w.Write(ctx, event, func(conn *sql.Conn) error {
		if err := checkRunEvent(ctx, conn, event); err != nil {
			return err
		}
		var err error
		meta, _, err = runstore.FinishWithDirectoriesTx(ctx, conn, in.Run, in.Status, in.Error, in.EndedAt, in.DirectoriesComplete)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		return w.FinishRequestIn(ctx, conn, "FinishRun", raw)
	})
	return meta, err
}

func runFinishRecoveryBinding(w *state.Writer) websvc.Binding {
	return websvc.Binding{Name: "FinishRun", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, in callspec.RunFinish) (runstore.Meta, error) {
		return writeRunFinish(ctx, w, in)
	}}
}
