package subagent

import (
	"context"
	"path/filepath"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/state"
)

// spool appends the session runner's queued calls through the shared transport.
func (m *Manager) spool(id string, queued []engineclient.Queued) error {
	dataDir := filepath.Dir(m.store().root())
	db, err := m.store().turnsDB()
	if err != nil {
		return err
	}
	return engineclient.AppendRunnerSpool(context.Background(), state.For(db), dataDir, id, queued)
}
