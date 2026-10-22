package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const daemonMarker = "DYNA_COMPUTERS_DAEMON"

// A Go test executable does
// not implement the CLI: starting it as a daemon can recursively run tests.
func checkDaemonSpawn(exe string) error {
	if testing.Testing() || strings.HasSuffix(filepath.Base(exe), ".test") {
		return fmt.Errorf("refusing to start the computers daemon from a Go test binary; use a fake daemon")
	}
	if os.Getenv(daemonMarker) == "1" {
		return fmt.Errorf("the computers daemon cannot start another computers daemon")
	}
	return nil
}
