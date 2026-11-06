package podman

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestProgressDoesNotCreditStdinReadAhead(t *testing.T) {
	// A real transport stand-in never reads stdin. At most its pipe capacity
	// (less framing) may count, even when os/exec reads ahead from the source.
	payload := bytes.Repeat([]byte("p"), 8<<20)
	frame := append([]byte(fmt.Sprintf("DYNA-IN %020d\n", len(payload))), payload...)
	var reported atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := (ExecRunner{}).RunProgress(ctx, frame, func(n int) { reported.Add(int64(n)) }, "env", "DYNA_TEST_STDIN_HELPER=1", exe, "-test.run=^TestProgressStdinHelper$")
	if err != nil {
		t.Fatalf("transport stand-in: %v; %s", err, out)
	}
	var capacity int64
	if _, err := fmt.Sscanf(string(out), "stdin pipe capacity %d", &capacity); err != nil {
		t.Fatalf("pipe capacity: %v; %q", err, out)
	}
	if capacity <= 29 || capacity >= int64(len(payload)) {
		t.Fatalf("pipe capacity %d does not bound this test's frame", capacity)
	}
	t.Logf("pipe capacity %d; reported payload %d", capacity, reported.Load())
	if reported.Load() > capacity-29 {
		t.Fatalf("credited source read-ahead: %d payload bytes, but only %d fit in transport pipe", reported.Load(), capacity-29)
	}
}

func TestProgressCountsCompleteTransportWrites(t *testing.T) {
	for _, size := range []int{0, 6, 1 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			frame := append([]byte(fmt.Sprintf("DYNA-IN %020d\n", size)), bytes.Repeat([]byte("p"), size)...)
			var reported atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := (ExecRunner{}).RunProgress(ctx, frame, func(n int) { reported.Add(int64(n)) }, "bash", "-c", "cat >/dev/null")
			if err != nil || reported.Load() != int64(size) {
				t.Fatalf("transport error=%v, progress=%d, wanted %d", err, reported.Load(), size)
			}
		})
	}
}

func TestProgressCopyJoinsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var reported atomic.Int64
	frame := append([]byte(fmt.Sprintf("DYNA-IN %020d\n", 8<<20)), make([]byte, 8<<20)...)
	_, err := (ExecRunner{}).RunProgress(ctx, frame, func(n int) {
		reported.Add(int64(n))
		cancel()
	}, "bash", "-c", "exec cat >/dev/null")
	if err == nil || ctx.Err() != context.Canceled || reported.Load() == 0 {
		t.Fatalf("copy did not join after progress cancelled the transport: error=%v, context=%v, progress=%d", err, ctx.Err(), reported.Load())
	}
}

func TestProgressSlowTransportExtendsIdleBudget(t *testing.T) {
	const budget = 750 * time.Millisecond
	const size = 4 << 20
	frame := append([]byte(fmt.Sprintf("DYNA-IN %020d\n", size)), make([]byte, size)...)
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events := make(chan struct{}, 1)
	ctx, stop := IdleContext(parent, budget, events)
	defer stop()
	var reported, callbacks atomic.Int64
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	// Disable the race runtime's one-second exit sleep in this child only: that
	// delay is not transport consumption, and would exceed this scaled idle budget.
	out, err := (ExecRunner{}).RunProgress(ctx, frame, func(n int) {
		reported.Add(int64(n))
		callbacks.Add(1)
		select {
		case events <- struct{}{}:
		default:
		}
	}, "env", "DYNA_TEST_STDIN_HELPER=slow", "GORACE=atexit_sleep_ms=0", exe, "-test.run=^TestProgressStdinHelper$")
	elapsed := time.Since(start)
	t.Logf("elapsed=%s, idle budget=%s, payload=%d, callbacks=%d, cause=%v", elapsed, budget, reported.Load(), callbacks.Load(), context.Cause(ctx))
	if err != nil || ctx.Err() != nil || reported.Load() != size || callbacks.Load() < 2 {
		t.Fatalf("slow-consuming transport lost incremental progress: error=%v, cause=%v, bytes=%d, callbacks=%d, output=%s", err, context.Cause(ctx), reported.Load(), callbacks.Load(), out)
	}
	if elapsed <= budget {
		t.Fatalf("transport completed in %s, not beyond the %s idle budget", elapsed, budget)
	}
}

func TestProgressStdinHelper(t *testing.T) {
	if os.Getenv("DYNA_TEST_STDIN_HELPER") == "" {
		t.Skip("subprocess helper")
	}
	if os.Getenv("DYNA_TEST_STDIN_HELPER") == "slow" {
		buffer := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				time.Sleep(10 * time.Millisecond)
			}
			if err == io.EOF {
				os.Exit(0)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	capacity, _, errno := syscall.Syscall(syscall.SYS_FCNTL, 0, syscall.F_GETPIPE_SZ, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	fmt.Printf("stdin pipe capacity %d", capacity)
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}
