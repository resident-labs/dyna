package visual

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// The kernel limits a unix socket path to 108 bytes. Chromium puts its process
// singleton socket under the temporary directory and aborts at start when that
// path is too long, which closes the debugging pipe before any message arrives.
const (
	chromiumSocketLimit = 108
	chromiumSocketTail  = "/org.chromium.Chromium.XXXXXX/SingletonSocket"
)

// browserTempDir makes the temporary directory for one Chromium process, from
// the first base that gives a short enough socket path.
func browserTempDir(bases []string) (string, error) {
	for _, base := range bases {
		dir, err := os.MkdirTemp(base, "dyna-visual-")
		if err != nil {
			continue
		}
		if len(dir)+len(chromiumSocketTail) < chromiumSocketLimit {
			return dir, nil
		}
		_ = os.RemoveAll(dir)
	}
	return "", fmt.Errorf("no temporary directory gives a Chromium socket path under %d bytes: tried %s", chromiumSocketLimit, strings.Join(bases, ", "))
}

// browserTempBases lists the temporary directories to try, the configured one
// first and then the usual short system paths.
func browserTempBases() []string {
	var bases []string
	for _, base := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		if base != "" && !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	return bases
}

// browserEnv is the caller's environment with TMPDIR and its aliases pointed at
// dir, so the browser never uses a long temporary directory of its own.
func browserEnv(dir string) []string {
	env := os.Environ()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		env = slices.DeleteFunc(env, func(v string) bool { return strings.HasPrefix(v, name+"=") })
	}
	return append(env, "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)
}
