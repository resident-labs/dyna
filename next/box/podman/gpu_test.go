package podman

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
		{"a comma in the name", "dyna-gpu-cdi 0\ndyna-gpu 0, NVIDIA, Inc. Card, 2048\n", []contract.GPU{{Index: 0, Name: "NVIDIA, Inc. Card", MemBytes: 2048 << 20}}},
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
		if strings.Contains(call[len(call)-1], " 'gpu' '/data/bin/current/dyna'") {
			probes++
		}
	}
	if probes < 3 {
		t.Fatalf("the host ran %d GPU probes", probes)
	}
}

// fakeNvidiaTools puts nvidia-ctk and nvidia-smi on PATH as scripts with the given bodies, since the probe runs them under timeout.
func fakeNvidiaTools(t *testing.T, ctk, smi string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"nvidia-ctk": ctk, "nvidia-smi": smi} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
}

// The script's own sed and pipes turn the tools' real output into what parseGPUs reads.
func TestGPUScriptReadsToolOutput(t *testing.T) {
	cmd := exec.Command("bash", "-c", gpuScript, "dyna", "/data", "/share", "gpu")
	cmd.Env = fakeNvidiaTools(t, `printf 'INFO: Found 2 CDI devices\nnvidia.com/gpu=0\nnvidia.com/gpu=1\nnvidia.com/gpu=all\n'`, `printf '0, NVIDIA GeForce RTX 3090 Ti, 24564\n1, GTX 1050 Ti, 4096\n'`)
	out, err := cmd.CombinedOutput()
	want := []contract.GPU{{Index: 0, Name: "NVIDIA GeForce RTX 3090 Ti", MemBytes: 24564 << 20}, {Index: 1, Name: "GTX 1050 Ti", MemBytes: 4096 << 20}}
	if err != nil || !reflect.DeepEqual(parseGPUs(out), want) {
		t.Fatalf("%s: %v: %+v", out, err, parseGPUs(out))
	}
	quiet := exec.Command("bash", "-c", gpuScript, "dyna", "/data", "/share", "-")
	quiet.Env = cmd.Env
	if out, err := quiet.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("a beat that does not probe printed %q: %v", out, err)
	}
}

// A tool that hangs must not hold the heartbeat's probe: the script gives each of them five seconds.
func TestGPUScriptEndsWhenAToolHangs(t *testing.T) {
	cmd := exec.Command("bash", "-c", gpuScript, "dyna", "/data", "/share", "gpu")
	cmd.Env = fakeNvidiaTools(t, "sleep 60", "sleep 60")
	started := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) != 0 || time.Since(started) > 20*time.Second {
		t.Fatalf("%q after %v: %v", out, time.Since(started), err)
	}
}

func TestParseAgy(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, out string
		want      *bool
	}{
		{"has agy", "dyna-agy 1\n", &yes},
		{"has none", "dyna-agy 0\n", &no},
		{"an older script prints no line: not probed", "dyna-gpu-cdi 0\n", nil},
		{"noise", "warning\ndyna-agy x\ndyna-agy 1\n", &yes},
	} {
		if got := parseAgy([]byte(tc.out)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// agyScript only stats: a program that is an executable file and a folder that is a directory make a host with agy, and nothing else does.
func TestAgyScriptStatsTheProgramAndTheFolder(t *testing.T) {
	dir := t.TempDir()
	program, account := dir+"/antigravity", dir+"/accounts-main"
	run := func(beat string) string {
		out, err := exec.Command("bash", "-c", agyScript, "dyna", "/data", "/mount", beat, "/bin/dyna", program, account).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	if got := run("gpu"); got != "dyna-agy 0" {
		t.Fatalf("nothing there: %q", got)
	}
	if err := os.WriteFile(program, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(account, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := run("gpu"); got != "dyna-agy 0" {
		t.Fatalf("a program that is not executable: %q", got)
	}
	if err := os.Chmod(program, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := run("gpu"); got != "dyna-agy 1" {
		t.Fatalf("both there: %q", got)
	}
	if got := run("-"); got != "" {
		t.Fatalf("a beat that does not probe printed %q", got)
	}
	if err := os.Remove(account); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(account, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run("gpu"); got != "dyna-agy 0" {
		t.Fatalf("the folder is a file: %q", got)
	}
}

// The agy fact rides the GPU probe's beats; a host whose script says nothing of agy (an older binary) stays "not probed".
func TestHeartbeatReportsAgyOnTheProbeBeat(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       *bool
	}{{"says yes", "dyna-agy 1\n", ptr(true)}, {"says no", "dyna-agy 0\n", ptr(false)}, {"says nothing", "dyna-gpu-cdi 0\n", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHost(t, &runner{body: tc.body})
			ctx, cancel := context.WithCancel(context.Background())
			ticks := make(chan time.Time)
			loads := make(chan Load, 8)
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.Heartbeat(ctx, ticks, box.Layout{HostData: "/data"}, func(ok bool, l Load) { loads <- l }, func(string, contract.PressureKind) {})
			}()
			for i := range 3 {
				ticks <- time.Unix(1000, 0).Add(time.Duration(i) * 10 * time.Second)
			}
			cancel()
			<-done
			close(loads)
			var got []*bool
			for l := range loads {
				got = append(got, l.Agy)
			}
			if len(got) < 2 || !reflect.DeepEqual(got[0], tc.want) || got[1] != nil {
				t.Fatalf("Agy on the two beats: %v, want %v then not probed", got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
