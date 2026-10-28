package connect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The connector follows its credential: a new link restarts the child, an
// unlink stops it, and the same token leaves it alone. The decision is pure, and
// the loop test uses fake ticks and a fake child, so neither has a clock, a
// process or a timeout.
func TestTunnelAlertsOnceWhenTheConnectorIsMissing(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}

	step := make(chan time.Time, 4)
	previousPause := connectorPause
	connectorPause = func(time.Duration) <-chan time.Time { return step }
	t.Cleanup(func() { connectorPause = previousPause })

	lookErrs := make(chan error, 4)
	previousLook := lookConnector
	lookConnector = func(string) (string, error) {
		select {
		case err := <-lookErrs:
			return "", err
		default:
			return "/resolved/cloudflared", nil
		}
	}
	t.Cleanup(func() { lookConnector = previousLook })

	type alert struct{ binary, path string }
	alerts := make(chan alert, 4)
	clears := make(chan struct{}, 4)
	previousPost, previousClear := postTunnelAlert, clearTunnelAlert
	postTunnelAlert = func(binary, path string) error { alerts <- alert{binary, path}; return nil }
	clearTunnelAlert = func() error { clears <- struct{}{}; return nil }
	t.Cleanup(func() { postTunnelAlert, clearTunnelAlert = previousPost, previousClear })

	started := make(chan struct{}, 4)
	previousRun := runConnector
	runConnector = func(ctx context.Context, _ *Store, bin string) error {
		if bin != "/resolved/cloudflared" {
			t.Errorf("connector ran %q, want the resolved path", bin)
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { runConnector = previousRun })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTunnel(ctx, st, "cloudflared", func(string, ...any) {})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The token is there but the binary is not: one typed alert, no child, and
	// the wait continues instead of retrying silently.
	lookErrs <- exec.ErrNotFound
	if a := <-alerts; a.binary != "cloudflared" || a.path == "" {
		t.Fatalf("alert = %+v, want the binary and the searched PATH", a)
	}
	step <- time.Now()
	lookErrs <- exec.ErrNotFound
	select {
	case a := <-alerts:
		t.Fatalf("a still-missing connector alerted again: %+v", a)
	case <-started:
		t.Fatal("the tunnel started a missing connector")
	case <-time.After(50 * time.Millisecond):
	}
	// The binary appears: the alert clears and the connector starts.
	step <- time.Now()
	<-clears
	<-started
}

func TestCredentialDiffersOnlyOnChange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		now   string
		err   error
		seen  string
		wants bool
	}{
		{"same token", "first", nil, "first", false},
		{"new token", "second", nil, "first", true},
		{"token removed", "", fs.ErrNotExist, "first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialDiffers([]byte(tc.now), tc.err, []byte(tc.seen)); got != tc.wants {
				t.Fatalf("credentialDiffers(%q, %v, %q) = %t, want %t", tc.now, tc.err, tc.seen, got, tc.wants)
			}
		})
	}
}

func TestTunnelFollowsItsCredential(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}

	attempts := make(chan chan time.Time, 8)
	previousTicks := tokenTicks
	tokenTicks = func() (<-chan time.Time, func()) {
		ticks := make(chan time.Time)
		attempts <- ticks
		return ticks, func() {}
	}
	t.Cleanup(func() { tokenTicks = previousTicks })

	events := make(chan string, 8)
	previousLook := lookConnector
	lookConnector = func(string) (string, error) { return "resolved", nil }
	t.Cleanup(func() { lookConnector = previousLook })

	previousPost, previousClear := postTunnelAlert, clearTunnelAlert
	postTunnelAlert = func(string, string) error { return nil }
	clearTunnelAlert = func() error { return nil }
	t.Cleanup(func() { postTunnelAlert, clearTunnelAlert = previousPost, previousClear })

	previousRun := runConnector
	runConnector = func(ctx context.Context, _ *Store, _ string) error {
		events <- "start"
		<-ctx.Done()
		events <- "stop"
		return ctx.Err()
	}
	t.Cleanup(func() { runConnector = previousRun })

	logs := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTunnel(ctx, st, "unused", func(format string, args ...any) { logs <- fmt.Sprintf(format, args...) })
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	first := <-attempts
	if event := <-events; event != "start" {
		t.Fatalf("first event = %q, want start", event)
	}
	// A new link writes a new token: the child holding the old tunnel stops, and
	// the restarted loop starts one with the new credential.
	if err := st.SaveTunnelToken("second"); err != nil {
		t.Fatal(err)
	}
	first <- time.Now()
	if event := <-events; event != "stop" {
		t.Fatalf("a new token did not cancel the running child: %q", event)
	}
	second := <-attempts
	if event := <-events; event != "start" {
		t.Fatalf("a new token did not start a child with the new credential: %q", event)
	}
	// An unlink removes the token: the child stops, and the loop reports that it
	// waits for a link instead of starting another connector.
	if err := os.Remove(st.TunnelTokenPath()); err != nil {
		t.Fatal(err)
	}
	second <- time.Now()
	if event := <-events; event != "stop" {
		t.Fatalf("an unlink did not cancel the running child: %q", event)
	}
	for {
		if message := <-logs; strings.Contains(message, "no tunnel token yet") {
			return
		}
	}
}
