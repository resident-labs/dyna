package computerweb

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/computers"
)

// daemon starts a fake daemon on a temp control socket.
func daemon(t *testing.T) (*computers.FakeSupervisor, *Host) {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "control.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fake := computers.NewFakeSupervisor("ses_a")
	go fake.Serve(ctx, l)
	h := &Host{
		Sessions: func(room string) ([]Session, error) {
			return []Session{{"ses_b", "beta"}, {"ses_a", "alpha"}}, nil
		},
		Client: func(s string) *computers.Client {
			if s == "ses_b" { // the fake answers for every session; this one has no computer
				return &computers.Client{Socket: filepath.Join(dir, "none.sock"), Session: s}
			}
			return &computers.Client{Socket: socket, Session: s}
		},
		Timeout: 2 * time.Second,
	}
	return fake, h
}

func TestComputersJoinsSessionsAndSortsRunningFirst(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake, h := daemon(t)
	fake.Current.State = computers.StateRunning
	fake.Current.Apps = []computers.App{{AppID: "firefox", Title: "Sign in"}}
	got, err := h.Computers("room")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Session != "ses_a" || got[0].State != "running" || got[0].Name != "alpha" || got[0].Apps[0].AppID != "firefox" {
		t.Fatalf("running first: %+v", got[0])
	}
	if got[1].Session != "ses_b" || got[1].State != "none" || got[1].Apps == nil {
		t.Fatalf("a session with no computer is none: %+v", got[1])
	}
}

func TestLiveCountsRunningComputersInEveryRoom(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake, h := daemon(t)
	fake.Current.State = computers.StateRunning
	if n, err := h.Live(t.Context()); err != nil || n != 1 {
		t.Fatalf("Live = %d, %v; want the running computer", n, err)
	}
	fake.Current.State = computers.StateCreated
	if n, err := h.Live(t.Context()); err != nil || n != 0 {
		t.Fatalf("Live = %d, %v; want no running computer", n, err)
	}
}

func TestComputersWithDaemonDown(t *testing.T) {
	h := &Host{
		Sessions: func(string) ([]Session, error) { return []Session{{"ses_a", "alpha"}}, nil },
		Client: func(s string) *computers.Client {
			return &computers.Client{Socket: "/nonexistent/control.sock", Session: s}
		},
	}
	got, err := h.Computers("room")
	if err != nil || len(got) != 1 || got[0].State != "none" {
		t.Fatalf("%+v %v", got, err)
	}
	if c, err := h.Computer("ses_a"); err != nil || c.State != "none" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestFromStatusExposesLatestIntent(t *testing.T) {
	intent := &computers.ActionIntent{Text: "Checking the search field", At: time.Now().UTC()}
	got := FromStatus(computers.Status{LatestStatus: intent}, "agent")
	if got.LatestStatus == nil || *got.LatestStatus != *intent {
		t.Fatalf("intent missing from app data: %+v", got)
	}
}

func TestStates(t *testing.T) {
	for in, want := range map[computers.State]string{
		computers.StateCreated: "idle", computers.StateRunning: "running",
		computers.StateIdleStopped: "stopped", "starting": "starting", computers.StateDestroyed: "none", "": "none",
	} {
		if got := stateOf(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestShotFallsBackToLastPNG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	h := &Host{Client: func(s string) *computers.Client { return &computers.Client{Socket: "/nonexistent", Session: s} }}
	p, err := h.Shot("ses_a", true)
	if err != nil || filepath.Base(p) != "last.png" || filepath.Base(filepath.Dir(p)) != "ses_a" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := h.Shot("../x", false); err == nil {
		t.Fatal("a bad session was accepted")
	}
}

func TestViewLeaseAndControl(t *testing.T) {
	// A stand-in for wayvnc, and a daemon that points the lease at it.
	// The engine's PATH is not the daemon's PATH. A working daemon stream
	// must not depend on the engine finding wayvnc.
	t.Setenv("PATH", t.TempDir())
	rfb := filepath.Join(t.TempDir(), "rfb.sock")
	rl, err := net.Listen("unix", rfb)
	if err != nil {
		t.Fatal(err)
	}
	defer rl.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := rl.Accept(); err == nil {
			accepted <- c
		}
	}()
	fake, h := daemon(t)
	inner := computers.NewFakeSupervisor("ses_a")
	inner.Changed = make(chan struct{}, 4)
	fake.Handle = func(ctx context.Context, r computers.Request) (any, error) {
		v, err := inner.Dispatch(ctx, r)
		if view, ok := v.(computers.View); ok {
			view.RFBSocket = rfb
			return view, nil
		}
		return v, err
	}
	view, err := h.OpenView("ses_a", "pagekey-1")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("wayvnc was not dialed")
	}
	if got, err := h.Control("ses_a", "pagekey-1", "human"); err != nil || got != "human" {
		t.Fatalf("take control: %q %v", got, err)
	}
	if _, err := h.Control("ses_b", "pagekey-1", "human"); err == nil {
		t.Fatal("a key controlled another session")
	}
	if _, err := h.Control("ses_a", "unknown-key", "human"); err == nil {
		t.Fatal("an unknown key controlled a computer")
	}
	if _, err := h.Control("ses_a", "pagekey-1", "root"); err == nil {
		t.Fatal("bad mode accepted")
	}
	if cur, _ := inner.Dispatch(context.Background(), computers.Request{Op: "status"}); cur.(computers.Status).Control != computers.ControlHuman {
		t.Fatalf("daemon control %q", cur.(computers.Status).Control)
	}
	// Closing the view gives control back: the lease ends with its connection.
	view.Done()
	// Every viewer change reports one token, so the release is an event.
	for {
		select {
		case <-inner.Changed:
		case <-time.After(2 * time.Second):
			cur, _ := inner.Dispatch(context.Background(), computers.Request{Op: "status"})
			status := cur.(computers.Status)
			t.Fatalf("control %q viewers %d after close", status.Control, status.Viewers)
		}
		cur, _ := inner.Dispatch(context.Background(), computers.Request{Op: "status"})
		status := cur.(computers.Status)
		if status.Control == computers.ControlAgent && status.Viewers == 0 {
			break
		}
	}
	if _, err := h.Control("ses_a", "pagekey-1", "agent"); err == nil {
		t.Fatal("a closed viewer still takes control calls")
	}
}

func TestViewFailureKeepsDaemonCode(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "wayvnc"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	fake, h := daemon(t)
	fake.Handle = func(context.Context, computers.Request) (any, error) {
		return nil, &computers.StreamError{Code: computers.StreamWayVNCMissing}
	}
	_, err := h.OpenView("ses_a", "pagekey-1")
	if err == nil {
		t.Fatal("view opened without a daemon stream")
	}
	var failure interface {
		FailureCode() string
		Permanent() bool
	}
	if !errors.As(err, &failure) || failure.FailureCode() != "wayvnc-missing" || !failure.Permanent() {
		t.Fatalf("daemon failure lost its code: %T %v", err, err)
	}
}

func TestSummarize(t *testing.T) {
	act := func(j string) computers.Action {
		var a computers.Action
		if err := json.Unmarshal([]byte(j), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	for in, want := range map[string]string{
		`{"type":"click","x":120,"y":40,"button":"left"}`:           "click · 120, 40",
		`{"type":"click","x":1,"y":2,"button":"right"}`:             "right click · 1, 2",
		`{"type":"double_click","x":5,"y":6}`:                       "double click · 5, 6",
		`{"type":"keypress","keys":["CTRL","a"]}`:                   "keypress · CTRL+a",
		`{"type":"type","text":"sam@example.dev"}`:                  `type · "sam@example.dev"`,
		`{"type":"type","text":"","secret":true,"chars":7}`:         "type · (secret)",
		`{"type":"type","text":"","chars":6}`:                       "type · 6 characters",
		`{"type":"type","text":""}`:                                 "type · (hidden)",
		`{"type":"screenshot"}`:                                     "screenshot",
		`{"type":"wait"}`:                                           "wait",
		`{"type":"scroll","x":1,"y":2,"scroll_x":0,"scroll_y":300}`: "scroll · down · 1, 2",
		`{"type":"scroll","x":1,"y":2,"scroll_y":-300}`:             "scroll · up · 1, 2",
		`{"type":"scroll","scroll_x":-5}`:                           "scroll · left",
		`{}`:                                                        "",
	} {
		if got := Summarize(act(in)); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
	long := Summarize(act(`{"type":"type","text":"` + strings.Repeat("a", 100) + `"}`))
	if !strings.HasSuffix(long, `…"`) {
		t.Errorf("long text not cut: %q", long)
	}
}

func TestRunningWithoutAWindowIsIdle(t *testing.T) {
	st := computers.Status{SessionID: "s", State: computers.StateRunning}
	if got := FromStatus(st, "n").State; got != "idle" {
		t.Fatalf("running with no apps must read idle, got %q", got)
	}
	st.Apps = []computers.App{{AppID: "foot"}}
	if got := FromStatus(st, "n").State; got != "running" {
		t.Fatalf("running with an app stays running, got %q", got)
	}
	st.Apps = nil
	st.State = computers.StateIdleStopped
	if got := FromStatus(st, "n").State; got != "stopped" {
		t.Fatalf("stopped stays stopped, got %q", got)
	}
}

// A session the room list does not name still gets its own status call.
func TestSessionMissingFromTheListGetsItsOwnStatus(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake, h := daemon(t)
	fake.Handle = func(ctx context.Context, r computers.Request) (any, error) {
		if r.Op == "list" {
			return []computers.Status{}, nil // the room filter dropped everything
		}
		return computers.Status{SessionID: r.Session, State: computers.StateRunning, Apps: []computers.App{{AppID: "foot"}}}, nil
	}
	h.Sessions = func(string) ([]Session, error) { return []Session{{"ses_a", "alpha"}}, nil }
	got, err := h.Computers("room")
	if err != nil || len(got) != 1 || got[0].State != "running" {
		t.Fatalf("%+v %v", got, err)
	}
}

// A daemon answer that names another session must never fill this row.
func TestStatusForAnotherSessionIsIgnored(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake, h := daemon(t)
	fake.Handle = func(ctx context.Context, r computers.Request) (any, error) {
		if r.Op == "list" {
			return []computers.Status{}, nil
		}
		return computers.Status{SessionID: "someone-else", State: computers.StateRunning, Apps: []computers.App{{AppID: "foot"}}}, nil
	}
	h.Sessions = func(string) ([]Session, error) { return []Session{{"ses_a", "alpha"}, {"ses_b", "beta"}}, nil }
	got, _ := h.Computers("room")
	for _, c := range got {
		if c.State != "none" {
			t.Fatalf("%s got another session's status: %+v", c.Session, c)
		}
	}
}
