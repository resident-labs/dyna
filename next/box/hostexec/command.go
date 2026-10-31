package hostexec

import (
	"fmt"
	"github.com/resident-labs/dyna/contract"
)

const OutputLimit = 8 << 20

func RunArgs(a contract.HostexecRunArgs, op contract.OpID) []string {
	cwd := a.Cwd
	if cwd == "" {
		cwd = "."
	}
	args := []string{"bash", "-c", `cd -- "$1" || exit 254; shift; "$@" 2>&1 | { head -c 8388609; cat >/dev/null; }; exit "${PIPESTATUS[0]}"`, "dyna-hostexec", cwd, "systemd-run", "--quiet", "--user", "--scope", "--unit=dyna-hostexec-" + string(op), "-p", fmt.Sprintf("RuntimeMaxSec=%g", float64(a.Timeout)/1000), "-p", "TimeoutStopSec=5", "--"}
	return append(args, a.Argv...)
}
