package localci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func remoteRunner(t *testing.T, legacy Command) (Runner, *fakeMachine) {
	t.Helper()
	runner := Runner{Node: &Node{Name: "pve", SSH: "pip@pve", Slots: 2, Enabled: true}}
	machine := newFakeMachine(t, legacy)
	machine.attach(&runner)
	return runner, machine
}

func testSpec(id string) detachSpec {
	return detachSpec{Dir: "/run/" + id + "/detached", Unit: id + ".service", Script: "true"}
}

// A dropped poll is not a check failure: the engine retries it, the check keeps
// running on the node, and its log arrives once.
func TestDetachedCheckSurvivesDroppedPolls(t *testing.T) {
	runner, machine := remoteRunner(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "building")
		return nil
	})
	drops := 5
	machine.down = func() bool {
		if len(machine.startCalls()) == 0 || drops == 0 {
			return false
		}
		drops--
		return true
	}
	poller, stop := runner.newPoller(t.Context())
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
	machine.down = func() bool { calls++; return calls == 1 }
	if err := runner.startDetached(t.Context(), testSpec("a")); err != nil {
		t.Fatal(err)
	}
	if got := len(machine.startCalls()); got != 1 {
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
	machine.down = func() bool { return len(machine.startCalls()) > 0 }
	poller, stop := runner.newPoller(t.Context())
	defer stop()
	err := poller.Run(t.Context(), testSpec("a"), io.Discard)
	var lost *nodeLostError
	if !errors.As(err, &lost) || lost.gone || !unreachable(*runner.Node, err) {
		t.Fatalf("err = %v, want a lost, unreachable node", err)
	}
}

// A unit that is gone without an exit record is a lost check, not a pass.
func TestDetachedCheckIsLostWhenItsUnitVanishes(t *testing.T) {
	var machine *fakeMachine
	runner, machine := remoteRunner(t, func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprintln(out, "partial")
		machine.vanishAll()
		<-ctx.Done()
		return ctx.Err()
	})
	poller, stop := runner.newPoller(t.Context())
	defer stop()
	var log bytes.Buffer
	err := poller.Run(t.Context(), testSpec("a"), &log)
	var lost *nodeLostError
	if !errors.As(err, &lost) || !lost.gone {
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
	poller, stop := runner.newPoller(t.Context())
	defer stop()
	err := poller.Run(t.Context(), testSpec("a"), io.Discard)
	if code, ok := commandExitStatus(err); !ok || code != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
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
			poller, stop := runner.newPoller(t.Context())
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
			if got := machine.stopCount(); got != want {
				t.Fatalf("stop calls = %d, want %d", got, want)
			}
		})
	}
}

// One poll call asks about every check of the node: the connection to a slow
// route is opened once per poll, not once per check.
func TestOnePollCoversEveryCheck(t *testing.T) {
	var calls [][]string
	runner := Runner{Node: &Node{Name: "pve", SSH: "pip@pve"}}
	runner.Command = func(_ context.Context, _ string, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
		_, positional, _ := decodeNodeScript(name, args)
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
	if _, err := parsePoll(strings.NewReader("DYNA_DETACH dir=/d state=running code= size=10 bytes=10\nhalf")); err == nil {
		t.Fatal("a cut reply was accepted")
	}
}
