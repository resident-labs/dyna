package hostexec

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/box/hostexec"
	"github.com/resident-labs/dyna/contract"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Local struct{ Socket string }

func (l Local) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	if l.Socket != "" && !a.Collect && !a.Attach {
		binary, err := os.Executable()
		if err != nil {
			return nil, 0, false, err
		}
		a.Argv = append([]string{"bash", "-c", `export DYNA_SOCKET="$1"; export PATH="$2:$PATH"; shift 2; exec "$@"`, "dyna-owner-cli", l.Socket, filepath.Dir(binary)}, a.Argv...)
	}
	args := hostexec.RunArgs(a, op)
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
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
