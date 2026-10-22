package agentslice

import (
	"os"
	"os/exec"
)

const foregroundScope = "DYNA_FOREGROUND_SCOPE"

// RunForeground runs cmd above agent work. It returns false when no user
// manager is available or the process is already in a foreground scope.
// The child inherits the marker so a desktop can relaunch itself once.
func RunForeground(cmd *exec.Cmd, name string) (bool, error) {
	if os.Getenv(foregroundScope) != "" {
		return false, nil
	}
	if wrap(cmd, name, ForegroundSlice, "CPUWeight="+ForegroundWeight, "IOWeight="+ForegroundWeight) == "" {
		return false, nil
	}
	cmd.Env = append(cmd.Environ(), foregroundScope+"="+name)
	return true, cmd.Run()
}
