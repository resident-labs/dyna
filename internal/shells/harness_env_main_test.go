package shells

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain keeps the caller's worker identity out of temporary test stores.
// It also lets the test binary act as dyna for Spawn: `shell _spawn` starts
// the supervisor (SPAWN_INNER overrides its executable), and `shell _run`
// reports its PID to SPAWN_READY and waits to be killed.
func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "shell" {
		switch os.Args[2] {
		case "_spawn":
			inner := os.Getenv("SPAWN_INNER")
			if inner == "" {
				inner = os.Args[0]
			}
			if err := StartSupervisor(inner, os.Args[3]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		case "_run":
			_ = os.WriteFile(os.Getenv("SPAWN_READY"), fmt.Appendf(nil, "%d\n", os.Getpid()), 0)
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
	// Tests set every Dyna option explicitly, including future options.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			os.Unsetenv(key)
		}
	}
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("MUSE_SESSION_ID")
	// Spawn waits for the `_spawn` launcher, a re-executed copy of this binary.
	// Keep race checks without its one-second exit grace. This process already
	// read GORACE, so only its children change.
	os.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	os.Exit(m.Run())
}
