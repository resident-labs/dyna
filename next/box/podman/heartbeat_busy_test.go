package podman

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// goneRunner is a host that went away: every command fails, the master's check and a new master too.
type goneRunner struct{}

func (goneRunner) Run(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("ssh: connect to host: no route to host")
}

func goneHost(t *testing.T) *Host {
	t.Helper()
	h, err := New("user@host", t.TempDir(), "host", goneRunner{})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// heartbeatReports runs a heartbeat over n ticks and returns its reports. Each tick waits for the one before it to finish,
// and the run may end during the last tick's probe, so only the first n-1 probes surely count.
func heartbeatReports(t *testing.T, h *Host, n int) []bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	reports := make(chan bool, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, _ Load) { reports <- ok }, func(string, contract.PressureKind) {})
	}()
	start := time.Unix(1000, 0)
	for i := range n {
		ticks <- start.Add(time.Duration(i) * 10 * time.Second)
	}
	cancel()
	<-done
	close(reports)
	var out []bool
	for ok := range reports {
		out = append(out, ok)
	}
	return out
}

// A host too loaded to run the probe in time still answers its master's keepalives. That is a busy host, not a lost one,
// however long it lasts.
func TestHeartbeatBusyHostIsNotUnreachable(t *testing.T) {
	r := &runner{fail: true}
	h := newTestHost(t, r)
	for _, ok := range heartbeatReports(t, h, 12) {
		if !ok {
			t.Fatal("a host whose master is alive was reported unreachable")
		}
	}
	checks := 0
	for _, call := range r.calls {
		for i, a := range call {
			if a == "-O" && i+1 < len(call) && call[i+1] == "check" {
				checks++
			}
		}
	}
	if checks < 2*11 {
		t.Fatalf("the heartbeat did not ask the master after failed probes: %d checks", checks)
	}
}

// A host that is really gone ends its master, so its misses count, and it is unreachable from the third.
func TestHeartbeatGoneHostIsUnreachable(t *testing.T) {
	got := heartbeatReports(t, goneHost(t), 6)
	if len(got) < 3 {
		t.Fatalf("reports %v, want unreachable from the third miss", got)
	}
	for _, ok := range got {
		if ok {
			t.Fatalf("reports %v, want only unreachable", got)
		}
	}
}

// An exec-only host's heartbeat only runs `true`: no probe script, no load, and the report says exec only.
func TestHeartbeatExecOnlyAsksOnlyThatTheHostAnswers(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	h.SetExecOnly(func() bool { return true })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	loads := make(chan Load, 4)
	go h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, load Load) { loads <- load }, func(string, contract.PressureKind) { t.Error("pressure") })
	ticks <- time.Unix(1000, 0)
	load := <-loads
	if !load.ExecOnly || load.DiskBytes != 0 || load.MemUsed != 0 {
		t.Fatalf("load %+v", load)
	}
	for _, call := range r.calls {
		for _, a := range call {
			if a == "dyna-disk" || a == "/data" {
				t.Fatalf("full probe ran: %v", call)
			}
		}
	}
}
