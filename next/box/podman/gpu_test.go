package podman

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestParseGPUsReadsCDIAndNvidiaSMI(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      []contract.GPU
	}{
		{"nothing", "", nil},
		{"one card", "dyna-gpu-cdi 0\ndyna-gpu 0, NVIDIA GeForce RTX 3090 Ti, 24564\n", []contract.GPU{{Index: 0, Name: "NVIDIA GeForce RTX 3090 Ti", MemBytes: 24564 << 20}}},
		{"two cards in index order", "dyna-gpu-cdi 1\ndyna-gpu-cdi 0\ndyna-gpu 1, GTX 1050 Ti, 4096\ndyna-gpu 0, RTX 2080 Ti, 11264\n", []contract.GPU{{Index: 0, Name: "RTX 2080 Ti", MemBytes: 11264 << 20}, {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4096 << 20}}},
		{"nvidia-smi sees a card CDI does not offer", "dyna-gpu-cdi 0\ndyna-gpu 0, A, 1024\ndyna-gpu 1, B, 2048\n", []contract.GPU{{Index: 0, Name: "A", MemBytes: 1024 << 20}}},
		{"CDI offers a card nvidia-smi does not report", "dyna-gpu-cdi 0\ndyna-gpu-cdi 1\ndyna-gpu 1, B, 2048\n", []contract.GPU{{Index: 0}, {Index: 1, Name: "B", MemBytes: 2048 << 20}}},
		{"no CDI spec", "dyna-gpu 0, A, 1024\n", nil},
		{"noise", "warning: something\ndyna-gpu-cdi x\ndyna-gpu-cdi 0\ndyna-gpu 0, A\ndyna-gpu 0, A, 1024\n", []contract.GPU{{Index: 0, Name: "A", MemBytes: 1024 << 20}}},
	} {
		if got := parseGPUs([]byte(tc.out)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestHeartbeatProbesGPUsOnTheFirstBeatAndEveryThirtieth(t *testing.T) {
	r := &runner{body: "dyna-gpu-cdi 0\ndyna-gpu 0, RTX, 1024\n"}
	h := newTestHost(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	loads := make(chan Load, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, l Load) { loads <- l }, func(string, contract.PressureKind) {})
	}()
	const beats = 62
	for i := range beats {
		ticks <- time.Unix(1000, 0).Add(time.Duration(i) * 10 * time.Second)
	}
	cancel()
	<-done
	close(loads)
	var probed []int
	i := 0
	for l := range loads {
		if l.GPUs != nil {
			probed = append(probed, i)
			if !reflect.DeepEqual(*l.GPUs, []contract.GPU{{Index: 0, Name: "RTX", MemBytes: 1 << 30}}) {
				t.Errorf("beat %d reported %+v", i, *l.GPUs)
			}
		}
		i++
	}
	if !reflect.DeepEqual(probed, []int{0, 30, 60}) {
		t.Fatalf("probed on beats %v of %d", probed, i)
	}
	probes := 0
	for _, call := range r.calls {
		if strings.HasSuffix(call[len(call)-1], " 'gpu'") {
			probes++
		}
	}
	if probes < 3 {
		t.Fatalf("the host ran %d GPU probes", probes)
	}
}

// The script's own sed and pipes turn the tools' real output into what parseGPUs reads.
func TestGPUScriptReadsToolOutput(t *testing.T) {
	script := `nvidia-ctk() { printf 'INFO: Found 2 CDI devices\nnvidia.com/gpu=0\nnvidia.com/gpu=1\nnvidia.com/gpu=all\n'; }; nvidia-smi() { printf '0, NVIDIA GeForce RTX 3090 Ti, 24564\n1, GTX 1050 Ti, 4096\n'; }; ` + gpuScript
	out, err := exec.Command("bash", "-c", script, "dyna", "/data", "/share", "gpu").CombinedOutput()
	want := []contract.GPU{{Index: 0, Name: "NVIDIA GeForce RTX 3090 Ti", MemBytes: 24564 << 20}, {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4096 << 20}}
	if err != nil || !reflect.DeepEqual(parseGPUs(out), want) {
		t.Fatalf("%s: %v: %+v", out, err, parseGPUs(out))
	}
	if out, err := exec.Command("bash", "-c", script, "dyna", "/data", "/share", "-").CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("a beat that does not probe printed %q: %v", out, err)
	}
}
