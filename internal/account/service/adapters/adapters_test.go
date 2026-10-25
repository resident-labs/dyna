package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeSubprocessTimeoutKillsProcessGroup(t *testing.T) {
	exe := claudeFixtureCommand(t, "sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := run(ctx, exe, nil); !errors.Is(err, ErrProbe) {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("process cleanup exceeded bound: %s", elapsed)
	}
}
func TestNativeSubprocessOutputBound(t *testing.T) {
	exe := claudeFixtureCommand(t, "head -c 4194305 /dev/zero\n")
	if output, err := run(context.Background(), exe, nil); !errors.Is(err, ErrProbe) || len(output) != 0 {
		t.Fatalf("unbounded output: bytes=%d err=%v", len(output), err)
	}
}
func TestSensitiveNativeErrorHasNoArguments(t *testing.T) {
	exe := claudeFixtureCommand(t, "echo PRIVATE_BODY;echo PRIVATE_ERROR >&2;exit 2\n")
	_, err := run(context.Background(), exe, nil, "PRIVATE_ARGUMENT")
	if err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("native output leaked", err)
	}
}
func TestNativeSubprocessUsesGivenEnvironment(t *testing.T) {
	exe := claudeFixtureCommand(t, "printf '%s' \"$DYNA_FIXTURE_HOME\"\n")
	output, err := run(context.Background(), exe, []string{"DYNA_FIXTURE_HOME=/private/home"})
	if err != nil || string(output) != "/private/home" {
		t.Fatalf("run() = %q, %v; want the given environment", output, err)
	}
}

// A failed probe must name the process outcome, never its output, arguments,
// or executable path, so the reason can reach the owner.
func TestNativeProbeFailureNamesTheOutcome(t *testing.T) {
	_, err := run(context.Background(), claudeFixtureCommand(t, "exit 3\n"), nil)
	if !errors.Is(err, ErrProbe) || !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("run() = %v; want the exit status named", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = run(ctx, claudeFixtureCommand(t, "sleep 30\n"), nil)
	if !errors.Is(err, ErrProbe) || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("run() = %v; want the timeout named", err)
	}
	broken := filepath.Join(t.TempDir(), "no-interpreter")
	if err := os.WriteFile(broken, []byte("#!/no/such/interpreter\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = run(context.Background(), broken, nil)
	if !errors.Is(err, ErrProbe) || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("run() = %v; want the start failure named", err)
	}
}
