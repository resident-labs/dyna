package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobstest"
)

func remoteRunner(t *testing.T, legacy jobstest.RunFunc) (Executor, *jobstest.Fake) {
	t.Helper()
	runner := Executor{Node: Conn{Name: "pve", SSH: "pip@pve"}}
	proto := jobstest.Protocol{Start: StartScript, Poll: PollScript, Stop: StopScript}
	machine := jobstest.NewFake(t, proto, legacy)
	runner.Command, runner.Wait, runner.Clock = Command(machine.CommandFunc()), machine.WaitFunc(), machine.ClockFunc()
	return runner, machine
}

func testSpec(id string) RunSpec {
	return RunSpec{Dir: "/run/" + id + "/detached", Unit: id + ".service", Script: "true"}
}

// exitError is a command that exited with a status, as a real command does.
func exitError(t *testing.T, code int) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
}

// A dropped poll is not a check failure: the engine retries it, the check keeps
// running on the node, and its log arrives once.
func TestDetachedCheckSurvivesDroppedPolls(t *testing.T) {
	runner, machine := remoteRunner(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "building")
		return nil
	})
	drops := 5
	machine.Down = func() bool {
		if len(machine.StartCalls()) == 0 || drops == 0 {
			return false
		}
		drops--
		return true
	}
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	var log bytes.Buffer
	if err := poller.Run(t.Context(), testSpec("a"), &log); err != nil {
		t.Fatalf("a dropped poll failed the check: %v", err)
	}
	if got := log.String(); got != "building\n" {
		t.Fatalf("log = %q, want it once", got)
	}
}

// A launch whose first attempt never reached the node is repeated.
func TestDetachedStartIsRetried(t *testing.T) {
	runner, machine := remoteRunner(t, func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error { return nil })
	calls := 0
	machine.Down = func() bool { calls++; return calls == 1 }
	if err := runner.StartDetached(t.Context(), testSpec("a")); err != nil {
		t.Fatal(err)
	}
	if got := len(machine.StartCalls()); got != 1 {
		t.Fatalf("starts = %d, want 1", got)
	}
}

// A node that stays unreachable past the bound loses its checks with the
// ssh-drop cause, so the run moves to another machine.
func TestDetachedCheckIsLostWhenTheNodeStaysUnreachable(t *testing.T) {
	runner, machine := remoteRunner(t, func(ctx context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	machine.Down = func() bool { return len(machine.StartCalls()) > 0 }
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	err := poller.Run(t.Context(), testSpec("a"), io.Discard)
	var lost *NodeLostError
	if !errors.As(err, &lost) || lost.Gone || lost.Node != "pve" {
		t.Fatalf("err = %v, want a lost, unreachable node", err)
	}
}

// A unit that is gone without an exit record is a lost check, not a pass.
func TestDetachedCheckIsLostWhenItsUnitVanishes(t *testing.T) {
	var machine *jobstest.Fake
	runner, machine := remoteRunner(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "partial")
		machine.VanishAll()
		<-ctx.Done()
		return ctx.Err()
	})
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	var log bytes.Buffer
	err := poller.Run(t.Context(), testSpec("a"), &log)
	var lost *NodeLostError
	if !errors.As(err, &lost) || !lost.Gone {
		t.Fatalf("err = %v, want a check without an exit record", err)
	}
	if log.String() != "partial\n" {
		t.Fatalf("log = %q, want what the check wrote before it vanished", log.String())
	}
}

// A non-zero status comes back as the check's own exit.
func TestDetachedCheckReportsItsExitStatus(t *testing.T) {
	runner, _ := remoteRunner(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		return exitError(t, 3)
	})
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	err := poller.Run(t.Context(), testSpec("a"), io.Discard)
	if code, ok := CommandExitStatus(err); !ok || code != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
	}
}

// The real stop protocol answers only after the log writer ended: a writer
// held past its wait bound fails the stop, so the run reconciles through its
// poll instead of trusting a log that can still grow, and a writer that ended
// lets the stop succeed. The script runs for real against a local wrapper
// process group and a held writer process.
func TestStopScriptFailsWhileTheLogWriterRuns(t *testing.T) {
	// The runner scrubs the environment, so the short writer bound rides in as
	// the first line of the same script: the stop text itself runs unchanged.
	stop := "DYNA_CI_WRITER_WAIT_TENTHS=3\n" + StopScript
	dir := t.TempDir()
	wrapper := exec.Command("setsid", "sleep", "60")
	if err := wrapper.Start(); err != nil {
		t.Fatal(err)
	}
	// The wrapper only stands in for a stopped check: end and reap it before
	// the calls, so a zombie group never looks running to the script.
	_ = wrapper.Process.Kill()
	_ = wrapper.Wait()
	writer := exec.Command("sleep", "60")
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = wrapper.Process.Kill()
		_ = writer.Process.Kill()
		_, _ = writer.Process.Wait()
	})
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(wrapper.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "writer.pid"), []byte(strconv.Itoa(writer.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	x := Executor{Node: Conn{Name: "here"}}
	var out, stderr TailBuffer
	err := x.NodeExec(t.Context(), nil, &out, &stderr, stop, dir, "dyna-924-test.unit")
	if err == nil || !strings.Contains(stderr.String(), "log writer") {
		t.Fatalf("stop with a held writer = %v, stderr %q; want the writer failure", err, stderr.String())
	}
	_ = writer.Process.Kill()
	_ = writer.Wait()
	var second TailBuffer
	if err := x.NodeExec(t.Context(), nil, &out, &second, stop, dir, "dyna-924-test.unit"); err != nil {
		t.Fatalf("stop after the writer ended = %v, stderr %q; want success", err, second.String())
	}
}

// Idle elapsed time comes from the node, so a skewed engine clock cannot
// stop an active job before its first node-side idle deadline.
func TestIdleTimeoutIgnoresEngineClockSkew(t *testing.T) {
	var machine *jobstest.Fake
	runner, machine := remoteRunner(t, func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
		for machine.PollCount() < 2 {
			runtime.Gosched()
		}
		return nil
	})
	runner.Clock = func() time.Time { return machine.ClockFunc()().Add(24 * time.Hour) }
	machine.OutputEveryPoll()
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	spec := testSpec("skew")
	spec.Idle = 5 * time.Second
	if err := poller.Run(t.Context(), spec, io.Discard); err != nil {
		t.Fatalf("engine clock skew ended the active job: %v", err)
	}
}

// Cancelling a run stops the unit on the node, but an engine stop leaves it
// running: the restarted engine reattaches.
func TestDetachedCancelStopsTheUnitButEngineStopDoesNot(t *testing.T) {
	for _, engineStop := range []bool{false, true} {
		t.Run(fmt.Sprint("engine stop ", engineStop), func(t *testing.T) {
			previous := EngineStopping()
			t.Cleanup(func() { engineStopping.Store(previous) })
			ClearEngineStopping()
			started := make(chan struct{})
			runner, machine := remoteRunner(t, func(ctx context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			})
			poller, stop := runner.NewPoller(t.Context())
			defer stop()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- poller.Run(ctx, testSpec("a"), io.Discard) }()
			<-started
			if engineStop {
				MarkEngineStopping()
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation", err)
			}
			want := 1
			if engineStop {
				want = 0
			}
			if got := machine.StopCount(); got != want {
				t.Fatalf("stop calls = %d, want %d", got, want)
			}
		})
	}
}

// One poll call asks about every check of the node: the connection to a slow
// route is opened once per poll, not once per check.
func TestOnePollCoversEveryCheck(t *testing.T) {
	var calls [][]string
	runner := Executor{Node: Conn{Name: "pve", SSH: "pip@pve"}}
	runner.Command = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		_, positional, _ := jobstest.DecodeNodeScript(name, args)
		calls = append(calls, positional)
		return nil
	}
	batch := []*watch{{spec: testSpec("a")}, {spec: testSpec("b")}, {spec: testSpec("c")}}
	if _, err := runner.pollNode(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 9 || !strings.Contains(strings.Join(calls[0], " "), "/run/c/detached") {
		t.Fatalf("poll calls = %v, want one call with three checks", calls)
	}
}

// A poll reply that ends inside a log is dropped whole, and the offset stays.
func TestPollReplyCutShortAdvancesNothing(t *testing.T) {
	if _, err := ParsePoll(strings.NewReader("DYNA_DETACH dir=/d state=running code= size=10 quiet_s=10 bytes=10\nhalf")); err == nil {
		t.Fatal("a cut reply was accepted")
	}
}

// Every attempt of a short call has a deadline of its own, so a connection that
// is up but silent returns to the retry loop instead of blocking all the checks
// of the node until the run ends.
func TestEveryNodeCallAttemptHasADeadline(t *testing.T) {
	var withDeadline, calls int
	runner := Executor{Node: Conn{Name: "pve", SSH: "pip@pve"}}
	runner.Command = func(ctx context.Context, _ string, _ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
		calls++
		if _, ok := ctx.Deadline(); ok {
			withDeadline++
		}
		return nil
	}
	if err := runner.StartDetached(t.Context(), testSpec("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.pollNode(t.Context(), []*watch{{spec: testSpec("a")}}); err != nil {
		t.Fatal(err)
	}
	if err := runner.StopDetached(t.Context(), testSpec("a")); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || withDeadline != 3 {
		t.Fatalf("%d of %d attempts had a deadline", withDeadline, calls)
	}
	_, args := runner.NodeProgram("bash", []string{"-c", "true"})
	if !strings.Contains(strings.Join(args, " "), "ServerAliveInterval=15") {
		t.Fatalf("ssh args %v lack a keepalive", args)
	}
}

// An engine stop that cuts a launch short leaves the check alone, because the
// launch may have reached the node, and records the intent first so the restarted
// engine finds it. Any other stop cleans up the half-started check.
func TestStopDuringLaunchKeepsTheCheckOnlyForAnEngineStop(t *testing.T) {
	for _, engineStop := range []bool{false, true} {
		t.Run(fmt.Sprint("engine stop ", engineStop), func(t *testing.T) {
			previous := EngineStopping()
			t.Cleanup(func() { engineStopping.Store(previous) })
			ClearEngineStopping()
			ctx, cancel := context.WithCancel(t.Context())
			stops := 0
			runner := Executor{Node: Conn{Name: "pve", SSH: "pip@pve"}}
			runner.Command = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
				script, _, _ := jobstest.DecodeNodeScript(name, args)
				switch script {
				case StartScript:
					if engineStop {
						MarkEngineStopping()
					}
					cancel()
					return context.Canceled
				case StopScript:
					stops++
				}
				return nil
			}
			poller, stop := runner.NewPoller(t.Context())
			defer stop()
			recorded := 0
			poller.Started = func(RunSpec) { recorded++ }
			if err := poller.Run(ctx, testSpec("a"), io.Discard); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			if recorded != 1 {
				t.Fatalf("launch intent recorded %d times, want once before the launch", recorded)
			}
			if want := map[bool]int{false: 1, true: 0}[engineStop]; stops != want {
				t.Fatalf("stop calls = %d, want %d", stops, want)
			}
		})
	}
}

// The node counts as lost after the bound in all. Attempts that stall for their
// whole timeout use up the window, and later attempts and waits only get what is
// left of it.
func TestLostNodeBoundCountsFailedAttemptTime(t *testing.T) {
	runner := Executor{Node: Conn{Name: "pve", SSH: "pip@pve"}}
	now := time.Unix(1_000_000, 0)
	runner.Clock = func() time.Time { return now }
	runner.Wait = func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil }
	var spent time.Duration
	attempts := 0
	err := runner.Retry(t.Context(), CallTimeout, func(ctx context.Context) error {
		attempts++
		deadline, _ := ctx.Deadline()
		// The attempt stalls until its deadline.
		stall := time.Until(deadline).Round(time.Second)
		now = now.Add(stall)
		spent += stall
		return errors.New("stalled")
	})
	var lost *NodeLostError
	if !errors.As(err, &lost) {
		t.Fatalf("err = %v, want a lost node", err)
	}
	if elapsed := now.Sub(time.Unix(1_000_000, 0)); elapsed > NodeLostBound+retryWait {
		t.Fatalf("gave up after %s, want about %s", elapsed, NodeLostBound)
	}
	if attempts < 2 || attempts > 3 {
		t.Fatalf("attempts = %d, want two or three stalled attempts in the window", attempts)
	}
}

// A check whose folder was removed after it launched can never finish, so the
// poll reports it lost instead of waiting for the run's deadline.
func TestDetachedCheckIsLostWhenItsFolderIsRemoved(t *testing.T) {
	var machine *jobstest.Fake
	runner, machine := remoteRunner(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "partial")
		machine.Remove(testSpec("a").Dir)
		<-ctx.Done()
		return ctx.Err()
	})
	poller, stop := runner.NewPoller(t.Context())
	defer stop()
	err := poller.Run(t.Context(), testSpec("a"), io.Discard)
	var lost *NodeLostError
	if !errors.As(err, &lost) || !lost.Gone {
		t.Fatalf("err = %v, want a lost check", err)
	}
}
