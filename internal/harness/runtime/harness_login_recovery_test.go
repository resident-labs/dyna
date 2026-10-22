package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPrivateLoginRecoveryStopsTerminalBeforeReleasingGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := testPrivateRuntime(t)
	term, err := a.terminals.OpenCommand([]string{"/bin/sh", "-c", "sleep 300"}, nil, t.TempDir(), 90, 24)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := 0
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": {terminalID: term.ID, flow: &NativeLogin{
		Env: []string{"TOKEN=secret-fixture"}, Warning: "personal login warning",
		Cancel: func() error {
			for _, current := range a.terminals.List("") {
				if current.ID == term.ID {
					t.Error("login gate released before terminal drained")
				}
			}
			cancelled++
			return errors.New("restore warning")
		},
	}}}
	rows := a.PrivateRuntimeLogins()
	if len(rows) != 1 || rows[0].AccountID != "saved" || rows[0].TerminalID != term.ID {
		t.Fatal(rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "secret-fixture") {
		t.Fatal("login environment exposed")
	}
	if err := a.CancelPrivateRuntimeLogin("saved"); err == nil || err.Error() != "restore warning" {
		t.Fatal(err)
	}
	if cancelled != 1 || len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatal("login recovery state survived cancellation")
	}
	if err := a.CancelPrivateRuntimeLogin("saved"); err != nil || cancelled != 1 {
		t.Fatal("cancellation was not idempotent", err)
	}
}

func TestMissingPrivateLoginTerminalReleasesGate(t *testing.T) {
	a := testPrivateRuntime(t)
	done := make(chan struct{})
	flow := &NativeLogin{Cancel: func() error { close(done); return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a.watchPrivateLogin(ctx, &privateRuntimeLogin{terminalID: "closed-terminal", flow: flow})
	select {
	case <-done:
	default:
		t.Fatal("missing terminal kept the login gate")
	}
}

func TestPrivateLoginWatcherUsesTerminalEvents(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			a := testPrivateRuntime(t)
			term, err := a.terminals.OpenCommand([]string{"/bin/sh", "-c", fmt.Sprintf("read line; exit %d", code)}, nil, t.TempDir(), 90, 24)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan string, 2)
			flow := &NativeLogin{
				Complete: func(context.Context) (NativeAccount, error) { result <- "complete"; return NativeAccount{}, nil },
				Cancel:   func() error { result <- "cancel"; return nil },
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.watchPrivateLogin(ctx, &privateRuntimeLogin{terminalID: term.ID, flow: flow})
			}()
			if err := a.terminals.Write(term.ID, []byte("finish\n")); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-result:
				want := "cancel"
				if code == 0 {
					want = "complete"
				}
				if got != want {
					t.Fatalf("got %s, want %s", got, want)
				}
				for _, current := range a.terminals.List("") {
					if current.ID == term.ID && !current.Exited {
						t.Fatal("gate released before native exit")
					}
				}
			case <-time.After(time.Second):
				t.Fatal("terminal exit did not wake login watcher")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("login watcher did not finish")
			}
		})
	}
}

func TestPrivateLoginShutdownStopsTerminalBeforeReleasingGate(t *testing.T) {
	a := testPrivateRuntime(t)
	term, err := a.terminals.OpenCommand([]string{"/bin/sh", "-c", "read line"}, nil, t.TempDir(), 90, 24)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": {terminalID: term.ID, flow: &NativeLogin{Cancel: func() error {
		for _, current := range a.terminals.List("") {
			if current.ID == term.ID {
				t.Error("shutdown released login gate before terminal stopped")
			}
		}
		cancelled = true
		return nil
	}}}}
	a.CancelPrivateLogins()
	if !cancelled || len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatal("shutdown kept login state")
	}
}

func TestPrivateLoginFinishOwnsCompletionBeforeWatcher(t *testing.T) {
	a := testPrivateRuntime(t)
	ctx := t.Context()
	// The timeout bounds a missing event; normal completion uses the channels.
	wait, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	term, err := a.terminals.OpenCommand([]string{"/bin/sh", "-c", "read line"}, nil, t.TempDir(), 90, 24)
	if err != nil {
		t.Fatal(err)
	}
	completing := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	cancelled := make(chan struct{}, 1)
	login := &privateRuntimeLogin{terminalID: term.ID, flow: &NativeLogin{
		Complete: func(context.Context) (NativeAccount, error) {
			for _, current := range a.terminals.List("") {
				if current.ID == term.ID {
					t.Error("finish released login gate before terminal stopped")
				}
			}
			close(completing)
			<-release
			return NativeAccount{Status: "ready"}, nil
		},
		Cancel: func() error { cancelled <- struct{}{}; return nil },
	}}
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": login}
	finished := make(chan error, 1)
	go func() { _, err := a.FinishPrivateRuntimeLogin(ctx, "saved"); finished <- err }()
	select {
	case <-completing:
	case <-wait.Done():
		t.Fatal("waiting for finish completion:", wait.Err())
	}
	// Complete is held, so Finish must still own the terminal/login boundary.
	if login.control.TryLock() {
		login.control.Unlock()
		t.Fatal("finish released completion ownership before Complete returned")
	}
	watching := make(chan struct{})
	go func() { defer close(watching); a.watchPrivateLogin(ctx, login) }()
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-wait.Done():
		t.Fatal("waiting for finish result:", wait.Err())
	}
	select {
	case <-watching:
	case <-wait.Done():
		t.Fatal("waiting for watcher result:", wait.Err())
	}
	select {
	case <-cancelled:
		t.Fatal("watcher canceled the login after manual completion")
	default:
	}
	if len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatal("finished login kept its recovery entry")
	}
}

func TestPrivateLoginFinishUsesRuntimeLifetime(t *testing.T) {
	a := testPrivateRuntime(t)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	a.SetLifetime(lifetime)
	request, cancel := context.WithCancel(context.Background())
	cancel()
	term, err := a.terminals.OpenCommand([]string{"/bin/sh", "-c", "read line"}, nil, t.TempDir(), 90, 24)
	if err != nil {
		t.Fatal(err)
	}
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": {terminalID: term.ID, flow: &NativeLogin{
		Complete: func(ctx context.Context) (NativeAccount, error) {
			if ctx != lifetime {
				t.Error("completion did not use runtime lifetime")
			}
			return NativeAccount{Status: "ready"}, ctx.Err()
		},
	}}}
	if _, err := a.FinishPrivateRuntimeLogin(request, "saved"); err != nil {
		t.Fatalf("request cancellation stopped native login completion: %v", err)
	}
}
