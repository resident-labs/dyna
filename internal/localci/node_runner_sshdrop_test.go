package localci

import (
	"context"
	"os/exec"
	"testing"
)

// sshDropError is the error an SSH client leaves when it loses the connection.
func sshDropError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 255").Run()
	if err == nil {
		t.Fatal("the fixture did not fail")
	}
	return err
}

// A check that wrote its own marker before the SSH client lost the connection
// must be classified as an SSH drop, not as an unclassified failure: the marker
// proves the command ran, and exit 255 proves the result never came back, so the
// run has to be retried rather than reported as a test failure.
func TestCheckCauseNamesAnSSHDropAfterTheCheckWroteItsMarker(t *testing.T) {
	node := Node{Name: "hermeslab", SSH: "pip@hermeslab"}
	markers := [][]string{{"dyna-ci-check-exit", "0"}}
	if cause := checkCause(node, context.Background(), sshDropError(t), markers, false); cause != CauseSSHDrop {
		t.Fatalf("cause = %q, want %q", cause, CauseSSHDrop)
	}
	// Without the marker the same exit is the same cause.
	if cause := checkCause(node, context.Background(), sshDropError(t), nil, false); cause != CauseSSHDrop {
		t.Fatalf("cause without a marker = %q, want %q", cause, CauseSSHDrop)
	}
	// A local node has no SSH client to drop, so the exit stays unexplained.
	if cause := checkCause(Node{Name: "this-machine"}, context.Background(), sshDropError(t), markers, false); cause != CauseUnclassified {
		t.Fatalf("cause on a local node = %q, want %q", cause, CauseUnclassified)
	}
}
