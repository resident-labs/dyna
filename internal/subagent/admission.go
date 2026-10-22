package subagent

import (
	"context"
	"encoding/json"
	"errors"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// AdmissionTurn reads the live canonical execution in the caller's snapshot.
// It does not repair records, import files, or resolve a working directory.
func AdmissionTurn(ctx context.Context, q state.Reader, id string, n int) (Meta, error) {
	var meta Meta
	if err := sessionlife.LoadStored(ctx, q, id, &meta); err != nil {
		return Meta{}, err
	}
	raw, _, err := storedTurn(ctx, q, id, n)
	if err != nil {
		return Meta{}, err
	}
	var turn Turn
	if err := json.Unmarshal(raw, &turn); err != nil {
		return Meta{}, err
	}
	if meta.ID != id || meta.Turns != n || meta.Status != StatusIdle && meta.Status != "" || !meta.Closed.IsZero() || turn.N != n || turn.Status != TurnRunning || turn.StopAsked {
		return Meta{}, errors.New("account admission needs the session's current running turn")
	}
	return meta, nil
}
