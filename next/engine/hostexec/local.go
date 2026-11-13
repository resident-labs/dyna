package hostexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
)

type Local struct{ Socket string }

func (l Local) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	args := hostexec.RunArgs(a, op)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if l.Socket != "" {
		binary, err := os.Executable()
		if err != nil {
			return nil, 0, false, err
		}
		// The scope and its detached runner inherit this environment, including collect and attach runs.
		cmd.Env = append(os.Environ(), "DYNA_SOCKET="+l.Socket, "PATH="+filepath.Dir(binary)+":"+os.Getenv("PATH"))
	}
	out, err := cmd.Output()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
		err = nil
	}
	truncated := len(out) > hostexec.OutputLimit
	if truncated {
		out = out[:hostexec.OutputLimit]
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
