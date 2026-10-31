package podman

import (
	"context"
	"errors"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"strings"
)

const HostexecCapture = `cd -- "$1" || exit 254; shift; "$@" 2>&1 | { head -c 8388609; cat >/dev/null; }; exit "${PIPESTATUS[0]}"`

func (h *Host) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	cwd := a.Cwd
	if cwd == "" {
		cwd = "."
	}
	args := []string{"bash", "-c", HostexecCapture, "dyna-hostexec", cwd, "systemd-run", "--quiet", "--user", "--scope", "--unit=dyna-hostexec-" + string(op), "-p", fmt.Sprintf("RuntimeMaxSec=%g", float64(a.Timeout)/1000), "-p", "TimeoutStopSec=5", "--"}
	args = append(args, a.Argv...)
	out, err := h.run(ctx, args...)
	code := 0
	var exit remoteExit
	if errors.As(err, &exit) {
		code = int(exit)
		err = nil
	}
	truncated := len(out) > 8<<20
	if truncated {
		out = out[:8<<20]
	}
	return out, code, truncated, err
}
func (h *Host) StopHostexec(ctx context.Context, scope string) error {
	out, err := h.run(ctx, "systemctl", "--user", "stop", scope)
	var exit remoteExit
	if errors.As(err, &exit) && strings.Contains(string(out), "not loaded") {
		return nil
	}
	return err
}
