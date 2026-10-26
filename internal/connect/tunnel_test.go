package connect

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// The connector follows its credential: a new link restarts the child, an
// unlink stops it, and the same token leaves it alone. The decision is pure, and
// the loop test uses fake ticks and a fake child, so neither has a clock, a
// process or a timeout.
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
