package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

func TestWakeDemandCoalescesAndHonorsCooldown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := machines.Machine{ID: "m", Name: "node", Enabled: true, SSH: "user@node", WakeCommand: []string{"/usr/bin/true"}}
	source := fakeMachines{byName: map[string]machines.Machine{"node": m}}
	s := &Service{Store: Store{DataDir: dir}, DataDir: dir, Machines: source}
	if err := s.admission().upsert(ctx, Demand{ID: "check", Node: "node", Room: "room"}, false); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var probes, runs atomic.Int32
	s.wakeProbe = func(ctx context.Context, node machines.Machine) (machines.Machine, error) {
		if probes.Add(1) == 1 {
			close(started)
			<-release
		}
		node.Health = machines.HealthOffline
		return node, nil
	}
	s.wakeRun = func(context.Context, []string) error { runs.Add(1); return nil }
	done := make(chan struct{})
	go func() { defer close(done); s.wakeNode(ctx, m) }()
	<-started
	s.wakeNode(ctx, m)
	close(release)
	<-done
	s.wakeNode(ctx, m)
	if runs.Load() != 1 || probes.Load() != 2 {
		t.Fatalf("runs=%d probes=%d", runs.Load(), probes.Load())
	}
}

func TestFailedWakeDoesNotProbeAndUsesCooldown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := machines.Machine{ID: "m", Name: "node", Enabled: true, SSH: "user@node", WakeCommand: []string{"/usr/bin/true"}, Health: machines.HealthOnline, LastProbe: time.Now()}
	s := &Service{Store: Store{DataDir: dir}, DataDir: dir, Machines: fakeMachines{byName: map[string]machines.Machine{"node": m}}}
	if err := s.admission().upsert(ctx, Demand{ID: "check", Node: "node", Room: "room"}, false); err != nil {
		t.Fatal(err)
	}
	var runs, probes int
	s.wakeRun = func(context.Context, []string) error { runs++; return errors.New("failure") }
	s.wakeProbe = func(_ context.Context, m machines.Machine) (machines.Machine, error) { probes++; return m, nil }
	s.wakeNode(ctx, m)
	s.wakeNode(ctx, m)
	if runs != 1 || probes != 0 {
		t.Fatalf("runs=%d probes=%d", runs, probes)
	}
}

func TestWakeNodeSkipsDisabledOrCancelledDemand(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := machines.Machine{ID: "m", Name: "node", Enabled: true, SSH: "user@node", WakeCommand: []string{"/usr/bin/true"}}
	source := fakeMachines{byName: map[string]machines.Machine{"node": m}}
	s := &Service{Store: Store{DataDir: dir}, DataDir: dir, Machines: source}
	queue := s.admission()
	if err := queue.upsert(ctx, Demand{ID: "check", Node: "node", Room: "room"}, false); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	s.wakeProbe = func(_ context.Context, node machines.Machine) (machines.Machine, error) {
		probes.Add(1)
		return node, nil
	}
	m.Enabled = false
	source.byName["node"] = m
	s.wakeNode(ctx, m)
	m.Enabled = true
	source.byName["node"] = m
	if err := queue.Release(ctx, "node", "check"); err != nil {
		t.Fatal(err)
	}
	s.wakeNode(ctx, m)
	if probes.Load() != 0 {
		t.Fatalf("probed %d times without eligible demand", probes.Load())
	}
}

func TestWakeNodeAdmissionRequiresFreshOnlineProbe(t *testing.T) {
	now := time.Unix(1000, 0)
	m := machines.Machine{Name: "node", Enabled: true, WakeCommand: []string{"/usr/bin/true"}, Health: machines.HealthOnline, LastProbe: now.Add(-time.Minute), WakeReady: true}
	q := nodeQueue{queued: []Job{{ID: "j", Node: "node", RoomID: "room", Lane: LaneQuick}}}
	for _, tc := range []struct {
		health machines.Health
		at     time.Time
		want   bool
	}{
		{machines.HealthOnline, now.Add(-time.Minute), false},
		{machines.HealthOffline, now, false},
		{machines.HealthOnline, now, true},
	} {
		m.Health, m.LastProbe = tc.health, tc.at
		admitted, reasons := map[string]bool{}, map[string]string{}
		needsWrite := false
		if err := passNode(context.Background(), nil, now, m.Name, m, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
			t.Fatal(err)
		}
		if admitted["j"] != tc.want {
			t.Fatalf("health=%s at=%v admitted=%v reason=%q", tc.health, tc.at, admitted["j"], reasons["j"])
		}
	}
}
