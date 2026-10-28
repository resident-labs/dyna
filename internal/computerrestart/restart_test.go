package computerrestart

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/userservices"
)

func TestQuiet(t *testing.T) {
	app := []computers.App{{AppID: "firefox"}}
	for name, tc := range map[string]struct {
		list  []computers.Status
		quiet bool
	}{
		"none":                        {nil, true},
		"stopped":                     {[]computers.Status{{State: computers.StateIdleStopped, Apps: app, IdleSeconds: 1}}, true},
		"created with an app":         {[]computers.Status{{State: computers.StateCreated, Apps: app}}, true},
		"created with a command":      {[]computers.Status{{State: computers.StateCreated, Runs: 1}}, false},
		"destroyed with an app":       {[]computers.Status{{State: computers.StateDestroyed, Apps: app}}, true},
		"app open":                    {[]computers.Status{{State: computers.StateRunning, Apps: app, IdleSeconds: 9999}}, false},
		"starting with an app":        {[]computers.Status{{State: computers.StateStarting, Apps: app}}, false},
		"recent input and no app":     {[]computers.Status{{State: computers.StateRunning, IdleSeconds: 5}}, true},
		"only idle computers":         {[]computers.Status{{State: computers.StateRunning, IdleSeconds: 60}, {State: computers.StateStarting, IdleSeconds: 0}}, true},
		"one app among idle desktops": {[]computers.Status{{State: computers.StateRunning, IdleSeconds: 60}, {State: computers.StateRunning, Apps: app}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if reason := Quiet(tc.list); (reason == "") != tc.quiet {
				t.Fatalf("Quiet = %q; want quiet=%v", reason, tc.quiet)
			}
		})
	}
}

// serveDaemon answers list with the given answer and every other operation
// with the bare unknown-operation error a daemon from before that operation
// gives.
func serveDaemon(t *testing.T, list []computers.Status, listErr error) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	socket := computers.ControlSocket("")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			if r.Op == "list" {
				return list, listErr
			}
			return nil, fmt.Errorf("unknown computer operation %q", r.Op)
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

// stubSystemctl records systemctl argv through a PATH-visible stub, so a test
// never reaches the real service manager. It returns the record path.
func stubSystemctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + record + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return record
}

// A daemon whose status list cannot be read keeps the restart deferred.
func TestRestartNowDefersWhenLegacyStatusUnreadable(t *testing.T) {
	serveDaemon(t, nil, errors.New("invalid session id"))
	reason, err := RestartNow(context.Background())
	if err != nil || !strings.Contains(reason, "predates protected foreground commands") {
		t.Fatalf("legacy restart reason=%q err=%v", reason, err)
	}
}

// D-419: a quiet daemon that predates atomic restart admission must restart
// through the legacy quiet check. Waiting for admission support it only gains
// by restarting deferred the update for hours and blocked every newer client.
func TestRestartNowRestartsQuietLegacyDaemon(t *testing.T) {
	serveDaemon(t, []computers.Status{}, nil)
	record := stubSystemctl(t)
	reason, err := RestartNow(context.Background())
	if err != nil || reason != "" {
		t.Fatalf("reason=%q err=%v; want a completed restart", reason, err)
	}
	got, rerr := os.ReadFile(record)
	if rerr != nil || !strings.Contains(string(got), "try-restart\n"+userservices.Computers) {
		t.Fatalf("restart call=%q (%v); want try-restart %s", got, rerr, userservices.Computers)
	}
}

// A daemon that predates admission but reports an open app keeps the restart
// deferred, with the reason its status list gives.
func TestRestartNowDefersLegacyDaemonWithOpenApp(t *testing.T) {
	serveDaemon(t, []computers.Status{{SessionID: "x", State: computers.StateRunning, Apps: []computers.App{{AppID: "a"}}}}, nil)
	record := stubSystemctl(t)
	reason, err := RestartNow(context.Background())
	if err != nil || !strings.Contains(reason, "1 app(s) open") {
		t.Fatalf("reason=%q err=%v; want the legacy status reason", reason, err)
	}
	if _, rerr := os.Stat(record); !os.IsNotExist(rerr) {
		t.Fatalf("deferred restart still called systemctl: %v", rerr)
	}
}

func TestWaitRestartsWhenAppsClose(t *testing.T) {
	dir := t.TempDir()
	if err := userservices.MarkRestartPending(dir); err != nil {
		t.Fatal(err)
	}
	busy := []computers.Status{{State: computers.StateRunning, Apps: []computers.App{{AppID: "x"}}}}
	lists := [][]computers.Status{busy, nil}
	events := make(chan computers.Event, 1)
	events <- computers.Event{}
	restarts := 0
	err := Wait(context.Background(), Deps{
		Dir:    dir,
		List:   func(context.Context) ([]computers.Status, error) { l := lists[0]; lists = lists[1:]; return l, nil },
		Events: func(context.Context) (<-chan computers.Event, error) { return events, nil },
		Restart: func(context.Context) (string, error) {
			restarts++
			return "", nil
		},
	})
	if err != nil || restarts != 1 || len(lists) != 0 {
		t.Fatalf("err=%v restarts=%d lists left=%d", err, restarts, len(lists))
	}
	if _, err := os.Stat(filepath.Join(dir, userservices.RestartPending)); !os.IsNotExist(err) {
		t.Fatalf("marker still present: %v", err)
	}
}

func TestWaitHonorsAtomicRestartAdmission(t *testing.T) {
	dir := t.TempDir()
	if err := userservices.MarkRestartPending(dir); err != nil {
		t.Fatal(err)
	}
	events := make(chan computers.Event, 1)
	events <- computers.Event{}
	attempts := 0
	err := Wait(context.Background(), Deps{
		Dir:    dir,
		List:   func(context.Context) ([]computers.Status, error) { return nil, nil },
		Events: func(context.Context) (<-chan computers.Event, error) { return events, nil },
		Restart: func(context.Context) (string, error) {
			attempts++
			if attempts == 1 {
				return "a command acquired a lease", nil
			}
			return "", nil
		},
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v; want one deferred and one completed restart", attempts, err)
	}
}

func TestWaitWithoutMarkerDoesNothing(t *testing.T) {
	err := Wait(context.Background(), Deps{Dir: t.TempDir(),
		List:    func(context.Context) ([]computers.Status, error) { t.Fatal("listed"); return nil, nil },
		Restart: func(context.Context) (string, error) { t.Fatal("restarted"); return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWaitKeepsMarkerWhenRestartFails(t *testing.T) {
	dir := t.TempDir()
	_ = userservices.MarkRestartPending(dir)
	err := Wait(context.Background(), Deps{Dir: dir,
		List:    func(context.Context) ([]computers.Status, error) { return nil, errors.New("no daemon") },
		Events:  func(context.Context) (<-chan computers.Event, error) { return nil, errors.New("no daemon") },
		Active:  func(context.Context) (bool, error) { return false, nil },
		Restart: func(context.Context) (string, error) { return "", errors.New("boom") }})
	if err == nil || !userservices.RestartIsPending(dir) {
		t.Fatalf("err=%v pending=%v", err, userservices.RestartIsPending(dir))
	}
}

// answers feeds Wait one list result per call.
type answer struct {
	list []computers.Status
	err  error
}

func waitWith(t *testing.T, answers []answer, events func(context.Context) (<-chan computers.Event, error), active bool, queryErrors ...error) (restarts int, listed int) {
	t.Helper()
	dir := t.TempDir()
	_ = userservices.MarkRestartPending(dir)
	err := Wait(context.Background(), Deps{Dir: dir, Recheck: time.Second, After: func(time.Duration) <-chan time.Time { ch := make(chan time.Time, 1); ch <- time.Time{}; return ch },
		List: func(context.Context) ([]computers.Status, error) {
			a := answers[listed]
			listed++
			return a.list, a.err
		},
		Events: events,
		Active: func(context.Context) (bool, error) {
			if len(queryErrors) > 0 {
				err := queryErrors[0]
				queryErrors = queryErrors[1:]
				return false, err
			}
			return active, nil
		},
		Restart: func(context.Context) (string, error) { restarts++; return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestWaitDoesNotRestartActiveServiceOnListError(t *testing.T) {
	busy := []computers.Status{{State: computers.StateRunning, Apps: []computers.App{{AppID: "x"}}}}
	events := func(context.Context) (<-chan computers.Event, error) { return nil, errors.New("down") }
	restarts, listed := waitWith(t, []answer{{nil, errors.New("timeout")}, {busy, nil}, {nil, nil}}, events, true)
	if restarts != 1 || listed != 3 {
		t.Fatalf("restarts=%d listed=%d; want a restart only after a quiet answer", restarts, listed)
	}
}

func TestWaitRestartsInactiveServiceOnListError(t *testing.T) {
	events := func(context.Context) (<-chan computers.Event, error) { return nil, errors.New("down") }
	if restarts, listed := waitWith(t, []answer{{nil, errors.New("no socket")}}, events, false); restarts != 1 || listed != 1 {
		t.Fatalf("restarts=%d listed=%d", restarts, listed)
	}
}

func TestWaitRecoversFromFailedSubscription(t *testing.T) {
	busy := []computers.Status{{State: computers.StateRunning, Apps: []computers.App{{AppID: "x"}}}}
	events := func(context.Context) (<-chan computers.Event, error) { return nil, errors.New("down") }
	if restarts, listed := waitWith(t, []answer{{busy, nil}, {nil, nil}}, events, true); restarts != 1 || listed != 2 {
		t.Fatalf("restarts=%d listed=%d; a failed subscription must not stall the wait", restarts, listed)
	}
}

func TestWaitResubscribesAfterStreamCloses(t *testing.T) {
	busy := []computers.Status{{State: computers.StateRunning, Apps: []computers.App{{AppID: "x"}}}}
	subscriptions := 0
	events := func(context.Context) (<-chan computers.Event, error) {
		subscriptions++
		ch := make(chan computers.Event, 1)
		if subscriptions == 1 {
			close(ch)
		} else {
			ch <- computers.Event{}
		}
		return ch, nil
	}
	restarts, _ := waitWith(t, []answer{{busy, nil}, {busy, nil}, {nil, nil}}, events, true)
	if restarts != 1 || subscriptions != 2 {
		t.Fatalf("restarts=%d subscriptions=%d", restarts, subscriptions)
	}
}

func TestWaitKeepsRestartPendingWhenServiceQueryFails(t *testing.T) {
	events := func(context.Context) (<-chan computers.Event, error) { return nil, errors.New("down") }
	restarts, listed := waitWith(t, []answer{{nil, errors.New("timeout")}, {nil, nil}}, events, false, errors.New("bus unreachable"))
	if restarts != 1 || listed != 2 {
		t.Fatalf("restarts=%d listed=%d; a failed systemd query must not allow a restart", restarts, listed)
	}
}
