package localci

import (
	"context"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/jobstest"
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

// Dyna uses its own master and verifies replies independently of SSH's exit
// status, so a dropped master cannot make a truncated reply succeed (D-166).
func TestNodeProgramUsesDynaConnectionMultiplexing(t *testing.T) {
	r := Runner{Node: &Node{Name: "pve", SSH: "fl-rea-pve"}}
	name, args := r.exec().NodeProgram("bash", []string{"-c", "true"})
	if name != "ssh" {
		t.Fatalf("program = %q, want ssh", name)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, option := range []string{" -o ControlMaster=auto ", " -o ControlPersist=10m ", " -o ControlPath="} {
		if !strings.Contains(joined, option) {
			t.Fatalf("ssh args %v lack %q: every node command must own its connection", args, strings.TrimSpace(option))
		}
	}
	// A local node still runs the program itself.
	if name, args := (Runner{Node: &Node{Name: "this-machine"}}).exec().NodeProgram("bash", []string{"-c", "true"}); name != "bash" || len(args) != 2 {
		t.Fatalf("local node lost its program: %q %v", name, args)
	}
}

// A node reached through a slow jump host dials with its own connect timeout, so
// a fixed ten seconds does not fail a healthy machine (D-195, D-197).
func TestNodeProgramUsesNodeConnectTimeout(t *testing.T) {
	r := Runner{Node: &Node{Name: "pve", SSH: "fl-rea-pve", SSHConnectTimeout: 60 * time.Second}}
	_, args := r.exec().NodeProgram("bash", []string{"-c", "true"})
	if !slices.Contains(args, "ConnectTimeout=60") || slices.Contains(args, "ConnectTimeout=10") {
		t.Fatalf("ssh args %v, want ConnectTimeout=60", args)
	}
}

// The commit push to a node goes over the same slow relay, so it dials with the
// node's connect timeout too, set for that one git command.
func TestPushCommitToNodeUsesNodeConnectTimeout(t *testing.T) {
	var pushed []string
	r := Runner{Node: &Node{Name: "pve", SSH: "fl-rea-pve", SSHConnectTimeout: 60 * time.Second}}
	r.Command = jobs.Command(jobstest.FrameCommand(func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "push") {
			pushed = append([]string{name}, args...)
		}
		return nil
	}))
	if err := r.exec().PushCommit(t.Context(), "/repo", "/var/tmp/cache", "abc123", ""); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(pushed, " ")
	for _, want := range []string{"ConnectTimeout=60", "ControlMaster=auto", "ControlPath=", "ControlPersist=10m", "fl-rea-pve:/var/tmp/cache", "abc123:refs/dyna/ci/abc123"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("git args = %q, missing %q", pushed, want)
		}
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
