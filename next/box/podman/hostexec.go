package podman

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"sync"
	"time"
)

const (
	// hostexecDialing bounds the host runs opening a connection at once. sshd's default MaxStartups 10:30:100 refuses
	// connections past 10 that have not authenticated, and some hosts ban an address that opens many at once.
	hostexecDialing = 4
	// hostexecSetup is how long a run holds its dialing slot. The runner shows nothing until a command ends, so the slot
	// frees after a handshake's time instead of at the handshake, and a running command holds none.
	hostexecSetup = 5 * time.Second
)

func (h *Host) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	return h.RunHostexecWithEnv(ctx, a, op, nil)
}

// RunHostexecWithEnv supplies engine-host environment through the SSH boundary.
func (h *Host) RunHostexecWithEnv(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID, env []string) ([]byte, int, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	select {
	case h.dialGate <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, false, ctx.Err()
	}
	var once sync.Once
	free := func() { once.Do(func() { <-h.dialGate }) }
	setup := time.AfterFunc(h.dialSetup, free)
	defer func() { setup.Stop(); free() }()
	// Each command has a connection of its own, so any number run at once and none waits on the master. A host that does
	// not answer fails the ssh, which the engine reports as the host lost.
	args := hostexec.RunArgs(a, op)
	if len(env) > 0 {
		args = append(append([]string{"env"}, env...), args...)
	}
	out, err := h.executeOver(ctx, h.direct(), nil, args...)
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

// StopHostexec stops a command's scope on the host, so it reaches the command whatever became of its connection.
func (h *Host) StopHostexec(ctx context.Context, scope string) error {
	select {
	case h.stopGate <- struct{}{}:
		defer func() { <-h.stopGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	out, err := h.runUnbounded(ctx, nil, "systemctl", "--user", "stop", scope)
	var exit remoteExit
	if errors.As(err, &exit) && strings.Contains(string(out), "not loaded") {
		return nil
	}
	return err
}
