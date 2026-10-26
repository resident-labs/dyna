package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// A report copy whose SSH client lost the connection keeps that cause, so the
// scheduler's machine retry applies to it. Before, every copy failure became
// reports-unavailable and a transport drop never reached the retry (D-170).
// Any other copy failure still leaves the reports unavailable.
func TestReportCopyFailureKeepsItsTransportCause(t *testing.T) {
	for name, tc := range map[string]struct {
		copyErr func(*testing.T) error
		want    FailureCause
	}{
		"ssh drop":      {sshDropError, CauseSSHDrop},
		"other failure": {func(*testing.T) error { return errors.New("tar: unexpected EOF") }, CauseReports},
	} {
		t.Run(name, func(t *testing.T) {
			runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "remote", SSH: "pip@host", Slots: 1, Enabled: true}}
			runner.Command = func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
				if fakeSystemdVersion(out, name, args) || fakeNodeGit(dir, out, name, args) {
					return nil
				}
				script := args[len(args)-1]
				if strings.Contains(script, "systemd-run") {
					fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
					return nil
				}
				if strings.Contains(script, "tar -C") {
					return tc.copyErr(t)
				}
				return nil
			}
			result, err := runner.Run(t.Context(), Request{Definition: testDynaDefinition(t), Repo: t.TempDir(), Suites: []Suite{"go-static"}, DeferSave: true})
			if err == nil || result.Cause != tc.want || len(result.Checks) != 1 || result.Checks[0].Cause != tc.want {
				t.Fatalf("run cause = %q, checks = %+v, err = %v; want cause %q", result.Cause, result.Checks, err, tc.want)
			}
		})
	}
}
