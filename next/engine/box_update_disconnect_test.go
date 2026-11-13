package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/harness/testutil"

	"github.com/resident-labs/dyna/contract"
)

// A stop drops the agent channel before Podman finishes cleaning up the container and pod.
// Cancellation can lose its reply, even though the agent has already gone away.
type disconnectedStopHost struct {
	dormantHost
	disconnect       func()
	entered, release chan struct{}
}

func (h *disconnectedStopHost) Stop(ctx context.Context, id contract.BoxID) error {
	if h.disconnect != nil {
		h.disconnect()
	}
	close(h.entered)
	select {
	case <-h.release:
		return h.dormantHost.Stop(ctx, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBoxUpdateDisconnectIsNotHostLoss(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "slow stop"
		if restart {
			name = "engine restart during stop"
		}
		t.Run(name, func(t *testing.T) {
			r := newGoalRig(t)
			r.exec("UPDATE boxes SET image='image'")
			r.s.SetBinary(newBinary)
			_, disconnectRoot := connectBox(t, r.s, r.ctx, r.root, newBinary)
			_, disconnectLead := connectBox(t, r.s, r.ctx, r.lead, newBinary)
			turn := r.begin(r.child)
			turns, disconnect := connectBox(t, r.s, r.ctx, r.child, oldBinary)
			turnInputs(t, turns)
			endTurn(t, r.s, r.ctx, r.child, turn, testJob)
			waitNotices(t, r.s, r.ctx, 1)
			if got := noticeText(t, r.s, r.ctx); got != heldByJob {
				t.Fatalf("update notice = %q", got)
			}
			if _, problem := r.s.ShellJobEnded(r.ctx, asSession(r.child), contract.ShellJobEndedRequest{Job: testJob, End: jobExited, Tail: "deployed"}); problem != nil {
				t.Fatal(problem)
			}
			waitWant(t, r.s, r.ctx, r.child, "stop")

			host := &disconnectedStopHost{disconnect: disconnect, entered: make(chan struct{}), release: make(chan struct{})}
			r.s.boxes = host
			resetLifecycle(r.s)
			engineCtx, cancel := context.WithCancel(r.ctx)
			defer func() {
				close(host.release)
				cancel()
				r.s.reconciler.Wait()
			}()
			if err := r.s.reconciler.Pass(engineCtx); err != nil {
				t.Fatal(err)
			}
			<-host.entered
			testutil.Eventually(t, 5*time.Second, "stopped agent disconnect", func() bool { return !r.s.Connected(r.child) })
			if restart {
				// The engine goes away after the stop killed the agent, before its reply arrived.
				cancel()
				r.s.reconciler.Wait()
				disconnectRoot()
				disconnectLead()
				r.restart()
				r.s.SetBinary(newBinary)
				connectBox(t, r.s, r.ctx, r.root, newBinary)
				connectBox(t, r.s, r.ctx, r.lead, newBinary)
				if got := boxWant(t, r.s, r.ctx, r.child); got != "stop" {
					t.Fatalf("stop intent after restart = %s", got)
				}
				host = &disconnectedStopHost{entered: make(chan struct{}), release: make(chan struct{})}
				r.s.boxes = host
				resetLifecycle(r.s)
				if err := r.s.reconciler.Pass(r.ctx); err != nil {
					t.Fatal(err)
				}
				<-host.entered
			}

			// Seed the disconnect clock, then outlast the normal 60 s host-loss wait.
			if err := r.s.reconciler.Pass(r.ctx); err != nil {
				t.Fatal(err)
			}
			r.advance(61 * time.Second)
			if err := r.s.reconciler.Pass(r.ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.s.FailureNotices(r.ctx); err != nil {
				t.Fatal(err)
			}
			if b := stateOf(t, r.s, r.ctx, r.child); b != contract.BoxReady {
				t.Errorf("planned stop became %s before its reply", b)
			}
			if got := r.failures(r.lead); len(got) != 0 {
				t.Errorf("planned update sent Sessions failed: %+v", got)
			}

			host.release <- struct{}{}
			r.s.reconciler.Wait()
			if b := stateOf(t, r.s, r.ctx, r.child); b != contract.BoxStopped {
				t.Fatalf("stop reply left box %s", b)
			}
			passLifecycle(t, r.s, r.ctx)
			if b := stateOf(t, r.s, r.ctx, r.child); b != contract.BoxStarting {
				t.Fatalf("job result did not restart updated box: %s", b)
			}
			if _, problem := r.s.BoxHello(r.ctx, asSession(r.child), contract.BoxHelloRequest{}); problem != nil {
				t.Fatal(problem)
			}
			turns, _ = connectBox(t, r.s, r.ctx, r.child, newBinary)
			if text := turnInputs(t, turns); !strings.Contains(text, "deployed") || strings.Contains(text, "An update is ready") {
				t.Fatalf("updated box inputs = %q", text)
			}
			var lost int
			if err := r.st.Read().QueryRowContext(r.ctx, `SELECT count(*) FROM events WHERE type='box.changed' AND json_extract(body,'$.session')=? AND json_extract(body,'$.to')='lost'`, r.child).Scan(&lost); err != nil {
				t.Fatal(err)
			}
			if lost != 0 {
				t.Errorf("planned update published %d lost transitions", lost)
			}
		})
	}
}
