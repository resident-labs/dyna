package podman

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type healthProbeState struct {
	good, masterDead bool
}

type healthProbeRunner struct {
	state atomic.Pointer[healthProbeState]
}

func (r *healthProbeRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	state := r.state.Load()
	for i, arg := range args {
		if arg == "-O" && i+1 < len(args) {
			if args[i+1] == "check" && state.masterDead {
				return nil, errors.New("master unavailable")
			}
			return nil, nil
		}
		if arg == "-M" {
			if state.masterDead {
				return nil, errors.New("connection unavailable")
			}
			return nil, nil
		}
	}
	if !state.good {
		return nil, errors.New("probe failed")
	}
	body := "dyna-disk 1000 400\ndyna-load 100 1000\ndyna-share up\ndyna-binary wanted\n" +
		"dyna-svc svc_1 ActiveState=active SubState=running NRestarts=2\n" +
		"dyna-cpus 8\ndyna-cpu 100 1000\ndyna-mem 16000 4000\n"
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}

func TestHeartbeatSchedulingThresholdKeepsSeparateReachabilityGrace(t *testing.T) {
	type status struct {
		reachable bool
		health    contract.HostHealth
	}
	reachableUnknown := []status{{true, contract.HostHealthUnknown}}
	reachableHealthy := []status{{true, contract.HostHealthy}}
	reachableDegraded := []status{{true, contract.HostDegraded}}
	unreachableDegraded := []status{{false, contract.HostDegraded}}
	type beat struct {
		good, masterDead bool
		want             []status
	}
	cases := []struct {
		name  string
		beats []beat
	}{
		{"live live dead", []beat{
			{want: reachableUnknown},
			{want: reachableUnknown},
			{masterDead: true, want: reachableDegraded}, // Third failed probe, first dead-master miss.
			{masterDead: true},
			{masterDead: true, want: unreachableDegraded},
		}},
		{"all dead", []beat{
			{masterDead: true},
			{masterDead: true},
			{masterDead: true, want: unreachableDegraded},
			{masterDead: true, want: unreachableDegraded},
		}},
		{"all busy", []beat{
			{want: reachableUnknown},
			{want: reachableUnknown},
			{want: reachableDegraded},
			{want: reachableDegraded},
		}},
		{"live master resets only reachability misses", []beat{
			{masterDead: true},
			{masterDead: true},
			{want: reachableDegraded},
			{masterDead: true},
			{masterDead: true},
			{masterDead: true, want: unreachableDegraded},
		}},
		{"good probe resets health and both counters", []beat{
			{good: true, want: reachableHealthy},
			{want: reachableHealthy},
			{want: reachableHealthy},
			{masterDead: true, want: reachableDegraded},
			{good: true, want: reachableHealthy},
			{masterDead: true},
			{masterDead: true},
			{masterDead: true, want: unreachableDegraded},
			{good: true, want: reachableHealthy},
			{want: reachableHealthy},
			{masterDead: true},
			{masterDead: true, want: reachableDegraded},
		}},
	}
	for _, execOnly := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("exec-only=%t/%s", execOnly, tc.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					runner := &healthProbeRunner{}
					runner.state.Store(&healthProbeState{})
					h, err := New("user@host", t.TempDir(), "h", runner, t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					h.SetExecOnly(func() bool { return execOnly })
					h.SetServices(func() []contract.ServiceID { return []contract.ServiceID{"svc_1"} })
					ctx, cancel := context.WithCancel(context.Background())
					ticks := make(chan time.Time)
					type report struct {
						reachable bool
						load      Load
					}
					reports := make(chan report, 32)
					done := make(chan struct{})
					go func() {
						defer close(done)
						h.Heartbeat(ctx, ticks, box.Layout{}, func(reachable bool, load Load) {
							reports <- report{reachable, load}
						}, func(string, contract.PressureKind) { t.Error("unexpected pressure event") })
					}()
					defer func() { cancel(); <-done }()
					synctest.Wait()
					if len(reports) != 0 {
						t.Fatal("health published before any probe")
					}
					var metrics Load
					goodBeats := 0
					for i, tick := range tc.beats {
						runner.state.Store(&healthProbeState{good: tick.good, masterDead: tick.masterDead})
						ticks <- time.Unix(int64(i+1)*10, 0)
						// Wait for this exact pass, not the following tick. This distinguishes
						// publication at failure three from an off-by-one at failure four.
						synctest.Wait()
						if len(reports) != len(tick.want) {
							t.Fatalf("tick %d: got %d reports, want %d", i+1, len(reports), len(tick.want))
						}
						if tick.good {
							if execOnly {
								metrics = Load{ExecOnly: true, CPUs: 8, MemBytes: 16000, MemUsed: 4000, DiskBytes: 1000, DiskUsed: 400}
							} else {
								metrics = Load{ActiveBinary: "wanted", MemUsed: 100, DiskBytes: 1000, DiskUsed: 400, ShareUp: true,
									Services: []box.ServiceUnit{{ID: "svc_1", State: box.UnitRunning, Restarts: 2}}}
								if goodBeats == 0 {
									var gpus []contract.GPU
									metrics.GPUs = &gpus
								}
							}
							goodBeats++
						} else {
							metrics.GPUs = nil
						}
						for _, want := range tick.want {
							got := <-reports
							expected := metrics
							expected.Health, expected.Probe = want.health, ProbeFailed
							if tick.good {
								expected.Probe = ProbeGood
							}
							if got.reachable != want.reachable || !reflect.DeepEqual(got.load, expected) {
								t.Fatalf("tick %d: reachable=%t load=%+v; want reachable=%t load=%+v", i+1, got.reachable, got.load, want.reachable, expected)
							}
						}
					}
				})
			})
		}
	}
}
