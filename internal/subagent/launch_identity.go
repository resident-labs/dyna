package subagent

import (
	"context"
	"os/exec"
	"path/filepath"

	"dyna-agent/internal/callerid"
)

// startDetached starts a detached _consolidate, _transfer or _resume-room
// process recorded with the identity of whoever ran the command, so it has
// exactly that caller's power (requirements item 3). The launcher resolves
// itself with the engine's rules, never from its environment.
func (m *Manager) startDetached(cmd *exec.Cmd, name, launcher string) error {
	dataDir := filepath.Dir(m.store().root())
	actor, err := callerid.Self(dataDir)
	if err != nil {
		return err
	}
	return callerid.Start(context.Background(), cmd, callerid.Launch{DataDir: dataDir, Name: name, Actor: actor, Launcher: launcher})
}
