package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// FrozenInstructions is the exact snapshot and covered-note boundary saved
// with the metadata, before the runner claims its startup input.
type FrozenInstructions struct {
	Frozen    instructions.Snapshot `json:"frozen"`
	Covered   []string              `json:"covered,omitempty"`
	Refreshed bool                  `json:"refreshed"`
}

func (m *Manager) RunnerInstructions(ctx context.Context, turn int) (FrozenInstructions, error) {
	id, err := runnerSession(ctx)
	if err != nil {
		return FrozenInstructions{}, err
	}
	return m.runnerInstructions(ctx, id, turn)
}

func (m *Manager) instructionMeta(meta Meta, turn Turn) (Meta, error) {
	if meta.Consolidating != "" && turn.TransferID == consolidationToken(meta.Consolidating, meta.ID) {
		c, err := m.consolidation(meta.RoomID, meta.Consolidating)
		if err != nil {
			return Meta{}, err
		}
		return m.consolidationPromptMeta(meta, c)
	}
	return meta, nil
}

func (m *Manager) runnerInstructions(ctx context.Context, id string, n int) (FrozenInstructions, error) {
	if n <= 0 {
		return FrozenInstructions{}, errors.New("instructions require a positive turn number")
	}
	store := m.store()
	var reply FrozenInstructions
	err := store.withLockContext(ctx, id, func() error {
		meta, err := store.ReadMeta(id)
		if err != nil {
			return err
		}
		if !meta.Closed.IsZero() {
			return ErrClosed
		}
		if meta.Turns != n {
			return errors.New("instructions do not name the session's current turn")
		}
		turn, err := store.ReadTurn(id, n)
		if err != nil {
			return err
		}
		if meta.Instructions != nil {
			reply.Frozen = *meta.Instructions
		}
		reply.Refreshed = instructions.Refresh(reply.Frozen, n == 1 || meta.HarnessSession == "", meta.InstructionsStale, turn.TransferID != "")
		if reply.Refreshed {
			charter, err := m.instructionMeta(meta, turn)
			if err != nil {
				return err
			}
			reply.Frozen = m.desiredInstructions(charter)
			reply.Covered = slices.Clone(meta.InstructionsNotes)
			meta.Instructions, meta.InstructionsStale, meta.InstructionsKnown, meta.InstructionsNotes = &reply.Frozen, false, nil, nil
			meta.InstructionsChangedAt = time.Time{}
		} else if err := m.noteInstructionsLocked(&meta, time.Now()); err != nil {
			return err
		}
		meta = normalizeMeta(meta)
		raw, err := json.Marshal(reply)
		if err != nil {
			return err
		}
		db, err := database.Open(filepath.Dir(store.root()))
		if err != nil {
			return err
		}
		w := state.For(db)
		if err := w.Write(ctx, sessionlife.Changed(id), func(conn *sql.Conn) error {
			if err := sessionlife.SaveTx(conn, id, meta); err != nil {
				return err
			}
			return w.FinishRequestIn(ctx, conn, "RunnerInstructions", raw)
		}); err != nil {
			return err
		}
		return store.metaWritten(meta)
	})
	if err != nil {
		return FrozenInstructions{}, err
	}
	return reply, nil
}

func (w *runnerWrites) instructions(ctx context.Context) (FrozenInstructions, error) {
	var reply FrozenInstructions
	err := w.callResult(ctx, "RunnerInstructions", func() error {
		var err error
		reply, err = w.m.runnerInstructions(ctx, w.id, w.n)
		return err
	}, &reply, w.n)
	return reply, err
}
