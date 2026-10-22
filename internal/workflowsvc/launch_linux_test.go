//go:build linux

package workflowsvc

import (
	"os"
	"os/exec"
	"testing"
)

func TestForegroundParentDeathCancelsCoordinator(t *testing.T) {
	listener := launchFixture(t, "cancel")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(executable, "launch-test-parent")
	parent.Env = os.Environ()
	parent.Stderr = os.Stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill() })
	conn, decoder, _ := acceptWorker(t, listener)
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	readLaunchEvent(t, conn, decoder, "canceling")
	readLaunchEvent(t, conn, decoder, "terminal")
	expectLaunchEOF(t, conn, decoder)
}
