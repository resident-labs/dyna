package hostexec

import (
	"context"
	"errors"
	"fmt"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"os/exec"
	"strings"
)

type Local struct{}

func (Local) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	cwd := a.Cwd
	if cwd == "" {
		cwd = "."
	}
	args := []string{"-c", podman.HostexecCapture, "dyna-hostexec", cwd, "systemd-run", "--quiet", "--user", "--scope", "--unit=dyna-hostexec-" + string(op), "-p", fmt.Sprintf("RuntimeMaxSec=%g", float64(a.Timeout)/1000), "-p", "TimeoutStopSec=5", "--"}
	out, err := exec.CommandContext(ctx, "bash", append(args, a.Argv...)...).Output()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
		err = nil
	}
	truncated := len(out) > 8<<20
	if truncated {
		out = out[:8<<20]
	}
	return out, code, truncated, err
}
func (Local) StopHostexec(ctx context.Context, scope string) error {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", scope).CombinedOutput()
	if err != nil && strings.Contains(string(out), "not loaded") {
		return nil
	}
	return err
}
