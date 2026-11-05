package podman

import (
	"context"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestBusyProbeReportsSchedulingHealth(t *testing.T) {
	r := &runner{fail: true}
	h := newTestHost(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	reports := make(chan Load, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, box.Layout{}, func(ok bool, l Load) {
			if !ok {
				t.Error("busy probe changed reachability")
			}
			reports <- l
		}, func(string, contract.PressureKind) {})
	}()
	for i := 0; i < 4; i++ {
		ticks <- time.Unix(int64(i), 0)
		select {
		case load := <-reports:
			want := contract.HostHealthUnknown
			if i >= 2 {
				want = contract.HostDegraded
			}
			if load.Health != want || load.Probe != ProbeFailed {
				t.Fatalf("probe %d: %+v", i+1, load)
			}
		case <-time.After(time.Second):
			t.Fatal("busy failures were invisible to scheduling health")
		}
	}
	r.fail = false
	ticks <- time.Unix(10, 0)
	good := <-reports
	if good.Health != contract.HostHealthy || good.Probe != ProbeGood {
		t.Fatalf("good probe did not reset: %+v", good)
	}
	cancel()
	<-done
}
