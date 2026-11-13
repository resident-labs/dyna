package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

type scopeRunner interface {
	RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error)
	StopHostexec(context.Context, string) error
}

func (b *Boxes) TerminalArgv(ctx context.Context, host contract.HostName, id contract.BoxID) ([]string, error) {
	if host == contract.ControlPlane && id == "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		return []string{"systemd-run", "--user", "--scope", "--quiet", "--", shell, "-l"}, nil
	}
	h, err := b.host(ctx, host)
	if err != nil {
		return nil, err
	}
	return h.TerminalArgv(id), nil
}

func (b *Boxes) ExecArgv(ctx context.Context, host contract.HostName, id contract.BoxID, argv ...string) ([]string, error) {
	h, err := b.host(ctx, host)
	if err != nil {
		return nil, err
	}
	return h.ExecArgv(id, argv...), nil
}

// engineScopeRunner uses SSH even for the engine host: setsid alone does not
// move a detached runner out of the engine service's cgroup during a restart.
type engineScopeRunner struct {
	*podman.Host
	env []string
}

func (r engineScopeRunner) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	return r.Host.RunHostexecWithEnv(ctx, a, op, r.env)
}

func (b *Boxes) scopeRunner(ctx context.Context, host contract.HostName) (scopeRunner, error) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range hosts {
		if record.EngineHost == nil || !*record.EngineHost {
			continue
		}
		if record.Name != host && host != contract.ControlPlane {
			continue
		}
		h, err := b.host(ctx, record.Name)
		if err != nil {
			return nil, err
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return engineScopeRunner{Host: h, env: []string{"DYNA_SOCKET=" + filepath.Join(b.Runtime, "owner.sock"), "PATH=" + filepath.Dir(binary) + ":" + os.Getenv("PATH")}}, nil
	}
	if host == contract.ControlPlane {
		return nil, fmt.Errorf("control-plane host exec requires a registered engine host")
	}
	return b.host(ctx, host)
}

func (b *Boxes) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	r, err := b.scopeRunner(ctx, a.Host)
	if err != nil {
		return nil, 0, false, err
	}
	return r.RunHostexec(ctx, a, op)
}

func (b *Boxes) StopHostexec(ctx context.Context, host contract.HostName, scope string) error {
	r, err := b.scopeRunner(ctx, host)
	if err != nil {
		return err
	}
	return r.StopHostexec(ctx, scope)
}
