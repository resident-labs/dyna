package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

func TestStartStepDeadlineNamesHostAndStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Boxes{}
		spec := lifecycle.BoxSpec{Box: "b", Host: "arch"}
		for _, step := range []contract.StartStep{contract.StartBuildCache, contract.StartShared, contract.StartSlice, contract.StartForward, contract.StartStack, contract.StartCreate, contract.StartSidecars, contract.StartContainer} {
			start := time.Now()
			err := b.startStep(context.Background(), spec, step, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
			var timeout box.StepTimeout
			if !errors.As(err, &timeout) || timeout.Host != "arch" || timeout.Step != step || time.Since(start) != box.StartStepBudget {
				t.Fatalf("%s: %v at %s", step, err, time.Since(start))
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := b.startStep(ctx, spec, contract.StartCreate, func(ctx context.Context) error { return ctx.Err() })
		var timeout box.StepTimeout
		if !errors.Is(err, context.Canceled) || errors.As(err, &timeout) {
			t.Fatalf("owner stop counted: %v", err)
		}
		if err := b.startStep(context.Background(), spec, contract.StartImage, func(context.Context) error { time.Sleep(4 * time.Minute); return nil }); err != nil {
			t.Fatalf("healthy compiler got setup budget: %v", err)
		}
	})
}

func TestStartStatusUsesSessionStepWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Boxes{}
		spec := lifecycle.BoxSpec{Box: "b", Host: "arch"}
		entered := time.Now()
		_ = b.startStep(context.Background(), spec, contract.StartBinary, func(ctx context.Context) error {
			time.Sleep(61 * time.Second)
			status := b.StartStatus("b")
			if status == nil || !status.StartedAt.Equal(entered) || status.Label(time.Now()) != "Starting on arch · updating Dyna on host (1 min)" {
				t.Fatalf("status: %+v", status)
			}
			return nil
		})
		_ = b.startStep(context.Background(), spec, contract.StartCreate, func(context.Context) error { return nil })
		status := b.StartStatus("b")
		if status.Step != contract.StartCreate || status.Label(time.Now()) != "Starting on arch" || !status.StartedAt.Equal(time.Now()) {
			t.Fatalf("step did not reset: %+v", status)
		}
	})
}

type sharedTransferRunner struct {
	uploads   atomic.Int32
	installed atomic.Bool
	release   chan struct{}
	entered   chan struct{}
}

func (r *sharedTransferRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	body := ""
	if strings.Contains(args[len(args)-1], "command -v zstd") {
		body = "gzip\n"
	}
	if strings.Contains(args[len(args)-1], "dyna-probe") && r.installed.Load() {
		body = strings.Repeat("a", 64) + "\n"
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}
func (r *sharedTransferRunner) RunProgress(ctx context.Context, in []byte, progress func(int), args ...string) ([]byte, error) {
	r.uploads.Add(1)
	close(r.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
	}
	// Eighteen minutes exceeds the old whole-create deadline; payload consumption keeps the idle clock alive.
	for range 9 {
		time.Sleep(2 * time.Minute)
		progress(1024)
	}
	r.installed.Store(true)
	return r.Run(ctx, nil, args...)
}

func TestSharedTransferProgressAndSubscriberStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &sharedTransferRunner{release: make(chan struct{}), entered: make(chan struct{})}
		host, err := podman.New("user@host", t.TempDir(), "arch", r)
		if err != nil {
			t.Fatal(err)
		}
		b := &Boxes{Layout: box.Layout{HostData: "/host"}, Version: "v2"}
		b.binOnce.Do(func() { b.binSum = strings.Repeat("a", 64) })
		b.binCacheOnce.Do(func() { b.binCache = podman.NewBinaryCache(func() ([]byte, error) { return []byte("engine"), nil }) })
		first, cancel := context.WithCancel(context.Background())
		done := make(chan error, 2)
		go func() { done <- b.installBinary(first, host) }()
		<-r.entered
		go func() { done <- b.installBinary(context.Background(), host) }()
		synctest.Wait()
		b.mu.Lock()
		refs := b.installs[host].refs
		b.mu.Unlock()
		if refs != 2 {
			t.Fatalf("subscribers %d", refs)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("stop: %v", err)
		}
		close(r.release)
		if err := <-done; err != nil {
			t.Fatalf("progressing shared install: %v", err)
		}
		if r.uploads.Load() != 1 {
			t.Fatalf("uploads %d", r.uploads.Load())
		}
		if err := b.installBinary(context.Background(), host); err != nil {
			t.Fatal(err)
		}
		if r.uploads.Load() != 1 {
			t.Fatal("exact SHA cache hit sent payload")
		}
	})
}
