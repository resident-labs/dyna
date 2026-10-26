package connect

import (
	"context"
	"os"
	"testing"
	"time"
)

// A new link or an unlink takes effect without an engine restart: a changed
// token cancels the running child, a removed one settles with no child, and an
// unchanged token leaves the child alone. Ticks and the child are fakes, so the
// test has no clock and no process.
func TestTunnelFollowsItsCredential(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	previousTicks := tokenTicks
	tokenTicks = func() (<-chan time.Time, func()) { return ticks, func() {} }
	t.Cleanup(func() { tokenTicks = previousTicks })

	events := make(chan string, 8)
	previousRun := runConnector
	runConnector = func(ctx context.Context, _ *Store, _ string) error {
		events <- "start"
		<-ctx.Done()
		events <- "stop"
		return ctx.Err()
	}
	t.Cleanup(func() { runConnector = previousRun })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTunnel(ctx, st, "unused", t.Logf)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	tick := func() {
		t.Helper()
		select {
		case ticks <- time.Now():
		case <-time.After(time.Second):
			t.Fatal("the connector never checked its token")
		}
	}
	next := func() string {
		t.Helper()
		select {
		case event := <-events:
			return event
		case <-time.After(5 * time.Second):
			t.Fatal("the connector loop stopped responding")
			return ""
		}
	}
	if first := next(); first != "start" {
		t.Fatalf("first event = %q, want start", first)
	}
	// The same token on a tick changes nothing: nothing stops within a bound that
	// only guards against a hang.
	tick()
	select {
	case event := <-events:
		t.Fatalf("an unchanged token changed the connector: %q", event)
	case <-time.After(100 * time.Millisecond):
	}
	// A new link writes a new token, so the child holding the old tunnel stops
	// and a second child starts with the new credential.
	if err := st.SaveTunnelToken("second"); err != nil {
		t.Fatal(err)
	}
	tick()
	if replaced := next(); replaced != "stop" {
		t.Fatalf("a new token did not cancel the running child: %q", replaced)
	}
	if restarted := next(); restarted != "start" {
		t.Fatalf("a new token did not start a child with the new credential: %q", restarted)
	}
	// An unlink removes the token: the child stops and none replaces it.
	if err := os.Remove(st.TunnelTokenPath()); err != nil {
		t.Fatal(err)
	}
	tick()
	if stopped := next(); stopped != "stop" {
		t.Fatalf("an unlink did not cancel the running child: %q", stopped)
	}
	select {
	case event := <-events:
		t.Fatalf("a connector started after the unlink: %q", event)
	default:
	}
}
