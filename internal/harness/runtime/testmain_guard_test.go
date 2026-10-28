package runtime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain refuses CLI arguments before any test runs. A test that starts
// this binary the way Dyna runs its own helper commands (piOAuthArgv) must
// never re-run the test suite as a child of itself.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		fmt.Fprintf(os.Stderr, "refusing CLI arguments; this is the test binary: %s\n", os.Args[1])
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// The Pi OAuth sign-in helper runs the current Dyna executable with CLI
// arguments (piOAuthArgv). Under go test that executable is this test binary,
// and without a guard one such start re-runs the whole test suite,
// multiplying processes until the host hits its limits. The test binary must
// refuse CLI arguments instead.
func TestTestBinaryRefusesCLIArguments(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "harness", "accounts", "_pi-signin", "--pi", "/fake/pi")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("test binary answered CLI arguments with %v, want exit status 2", err)
		}
	case <-time.After(time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("test binary ran the suite instead of refusing CLI arguments")
	}
}
