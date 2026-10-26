package computerrestart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		Restart: func(context.Context) error {
			restarts++
			return nil
		},
	})
	if err != nil || restarts != 1 || len(lists) != 0 {
		t.Fatalf("err=%v restarts=%d lists left=%d", err, restarts, len(lists))
	}
	if _, err := os.Stat(filepath.Join(dir, userservices.RestartPending)); !os.IsNotExist(err) {
		t.Fatalf("marker still present: %v", err)
	}
}

func TestWaitWithoutMarkerDoesNothing(t *testing.T) {
	err := Wait(context.Background(), Deps{Dir: t.TempDir(),
		List:    func(context.Context) ([]computers.Status, error) { t.Fatal("listed"); return nil, nil },
		Restart: func(context.Context) error { t.Fatal("restarted"); return nil }})
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
		Restart: func(context.Context) error { return errors.New("boom") }})
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
		Restart: func(context.Context) error { restarts++; return nil }})
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
