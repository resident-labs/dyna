package connect

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// alerts records the posts and clears a test drives out of RunTunnel. The
// fake clock, lookup and pause keep every step synchronous, so no test waits
// for a real timeout.
type alerts struct {
	posted   chan [2]string
	cleared  chan struct{}
	failPost chan error // one-shot errors the next post returns, if any
}

func newAlerts() *alerts {
	return &alerts{posted: make(chan [2]string, 8), cleared: make(chan struct{}, 8), failPost: make(chan error, 8)}
}

func (a *alerts) install(t *testing.T) {
	t.Helper()
	post, clear := postTunnelAlert, clearTunnelAlert
	postTunnelAlert = func(binary, path string) error {
		select {
		case err := <-a.failPost:
			return err
		default:
		}
		a.posted <- [2]string{binary, path}
		return nil
	}
	clearTunnelAlert = func() error { a.cleared <- struct{}{}; return nil }
	t.Cleanup(func() { postTunnelAlert, clearTunnelAlert = post, clear })
}

// noPost asserts non-blockingly that no alert arrived. The caller must have
// synchronized on the lookup whose branch could post first.
func (a *alerts) noPost(t *testing.T) {
	t.Helper()
	select {
	case got := <-a.posted:
		t.Fatalf("unexpected alert %v", got)
	default:
	}
}

// startTunnel runs RunTunnel with a saved token, a fake lookup the test
// answers, a fake pause the test steps, and a connector that blocks until its
// context ends. Cleanup cancels and waits for the loop.
func startTunnel(t *testing.T, st *Store, look func(string) (string, error)) (step chan time.Time, started chan string) {
	t.Helper()
	step = make(chan time.Time, 4)
	previousPause := connectorPause
	connectorPause = func(time.Duration) <-chan time.Time { return step }
	t.Cleanup(func() { connectorPause = previousPause })
	previousLook := lookConnector
	lookConnector = look
	t.Cleanup(func() { lookConnector = previousLook })
	started = make(chan string, 4)
	previousRun := runConnector
	runConnector = func(ctx context.Context, _ *Store, bin string) error {
		started <- bin
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
	return step, started
}

// A missing connector posts one typed alert, does not post it again while it
// stays missing, and clears it once the connector runs on the resolved path.
func TestTunnelAlertsOnceWhenTheConnectorIsMissing(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}
	a := newAlerts()
	a.install(t)

	// calls synchronizes each lookup, so the assertion after the second call
	// cannot race the branch that would post.
	calls := make(chan error, 4)
	failures := make(chan error, 4)
	step, started := startTunnel(t, st, func(string) (string, error) {
		select {
		case err := <-failures:
			calls <- err
			return "", err
		default:
			calls <- nil
			return "/resolved/cloudflared", nil
		}
	})

	// The token is there but the binary is not: one typed alert, no child.
	failures <- exec.ErrNotFound
	if got := <-calls; got == nil {
		t.Fatal("first lookup succeeded with a missing binary")
	}
	if alert := <-a.posted; alert[0] != "cloudflared" || alert[1] == "" {
		t.Fatalf("alert = %v, want the binary and the searched PATH", alert)
	}
	step <- time.Now()
	// A still-missing connector does not alert again.
	failures <- exec.ErrNotFound
	if got := <-calls; got == nil {
		t.Fatal("second lookup succeeded with a missing binary")
	}
	a.noPost(t)
	step <- time.Now()
	// The binary appears: the alert clears and the connector starts.
	if got := <-calls; got != nil {
		t.Fatalf("third lookup failed: %v", got)
	}
	<-a.cleared
	if bin := <-started; bin != "/resolved/cloudflared" {
		t.Fatalf("connector ran %q, want the resolved path", bin)
	}
}

// A transient store failure must not suppress the alert: the next attempt
// posts again, and only a delivered post stops the retries.
func TestTunnelRetriesAFailedAlertPost(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}
	a := newAlerts()
	a.install(t)

	step, _ := startTunnel(t, st, func(string) (string, error) { return "", exec.ErrNotFound })

	a.failPost <- errors.New("store busy")
	step <- time.Now()
	if alert := <-a.posted; alert[0] != "cloudflared" {
		t.Fatalf("the retry did not post: %v", alert)
	}
}

// A fresh process that finds the connector clears an alert an earlier process
// left, so the notice cannot outlive the fault across a service restart.
func TestTunnelClearsAStaleAlertOnStartup(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTunnelToken("first"); err != nil {
		t.Fatal(err)
	}
	a := newAlerts()
	a.install(t)

	_, started := startTunnel(t, st, func(string) (string, error) { return "/resolved/cloudflared", nil })

	<-a.cleared
	a.noPost(t)
	if bin := <-started; bin != "/resolved/cloudflared" {
		t.Fatalf("connector ran %q", bin)
	}
}

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
