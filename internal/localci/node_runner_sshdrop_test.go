package localci

import (
	"context"
	"os/exec"
	"strings"
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

// Every ssh command a node run starts owns its end-to-end connection. An alias
// configured with ControlMaster hands commands a stream the shared master may
// have truncated when it died after the remote exit status arrived, and ssh
// then reports success. A run must never trust a multiplexed stream it did not
// open: a report fetch archived that way ended mid-entry and turned every check
// of the run into an infrastructure error (D-166).
func TestNodeProgramOptsOutOfConnectionMultiplexing(t *testing.T) {
	r := Runner{Node: &Node{Name: "pve", SSH: "fl-rea-pve"}}
	name, args := r.nodeProgram("bash", []string{"-c", "true"})
	if name != "ssh" {
		t.Fatalf("program = %q, want ssh", name)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, option := range []string{" -o ControlMaster=no ", " -o ControlPath=none "} {
		if !strings.Contains(joined, option) {
			t.Fatalf("ssh args %v lack %q: every node command must own its connection", args, strings.TrimSpace(option))
		}
	}
	// A local node still runs the program itself.
	if name, args := (Runner{Node: &Node{Name: "this-machine"}}).nodeProgram("bash", []string{"-c", "true"}); name != "bash" || len(args) != 2 {
		t.Fatalf("local node lost its program: %q %v", name, args)
	}
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

