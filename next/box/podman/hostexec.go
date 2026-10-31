package podman

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"strings"
)

func (h *Host) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	out, err := h.runUnbounded(ctx, nil, hostexec.RunArgs(a, op)...)
	code := 0
	var exit remoteExit
	if errors.As(err, &exit) {
		code = int(exit)
		err = nil
	}
	truncated := len(out) > hostexec.OutputLimit
	if truncated {
		out = out[:hostexec.OutputLimit]
	}
	return out, code, truncated, err
}
func (h *Host) StopHostexec(ctx context.Context, scope string) error {
	out, err := h.runUnbounded(ctx, nil, "systemctl", "--user", "stop", scope)
	var exit remoteExit
	if errors.As(err, &exit) && strings.Contains(string(out), "not loaded") {
		return nil
	}
	return err
}
