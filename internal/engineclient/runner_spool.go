package engineclient

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// SpoolLine is one write a runner could not send.
type SpoolLine struct {
	ID     string            `json:"id"`
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
	// At is when it was spooled. The engine applies a line only while
	// request IDs from that time are still remembered (state.RequestKeep).
	At time.Time `json:"at"`
}

// AppendRunnerSpool appends queued calls under the runner's spool lock.
// It preserves their order and request IDs and syncs each line before return.
// Session and workflow coordinators share this transport log and wire format.
func AppendRunnerSpool(ctx context.Context, w *state.Writer, dataDir, id string, queued []Queued) error {
	dir := sessionlife.RunnerSpoolDir(dataDir)
	unlock, err := sessionlife.LockRunnerSpool(ctx, dir, id)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, id+".jsonl")
	now := time.Now()
	for _, q := range queued {
		line := SpoolLine{ID: q.ID, Method: q.Method, At: now}
		for _, arg := range q.Args {
			raw, err := json.Marshal(arg)
			if err != nil {
				return err
			}
			line.Args = append(line.Args, raw)
		}
		b, err := json.Marshal(line)
		if err != nil {
			return err
		}
		if err := w.AppendSynced(ctx, hub.Event{}, path, b); err != nil {
			return err
		}
	}
	return nil
}
