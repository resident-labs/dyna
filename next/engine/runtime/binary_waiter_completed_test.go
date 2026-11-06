package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
)

type completedWaiterRunner struct {
	sum     string
	entered chan context.Context
	release chan struct{}
	result  string
}

func (r *completedWaiterRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	body, status := "", 0
	command := args[len(args)-1]
	if strings.Contains(command, "'dyna-probe'") {
		body = r.sum + "\n"
	}
	if strings.Contains(command, "'dyna-link'") {
		r.entered <- ctx
		<-r.release
		switch r.result {
		case "remote failure":
			status = 42
		case "transport failure":
			return nil, errors.New("activation transport failed without a frame")
		}
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
}

func newCompletedWaiter(t *testing.T, result string) (*Boxes, *podman.Host, *completedWaiterRunner, func()) {
	t.Helper()
	runner := &completedWaiterRunner{sum: strings.Repeat("a", 64), entered: make(chan context.Context), release: make(chan struct{}), result: result}
	host, err := podman.New("owner@host", t.TempDir(), "h", runner)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Context: context.Background(), Version: "waiter-test", Layout: box.Layout{HostData: "/host"}}
	b.binOnce.Do(func() { b.binSum = runner.sum })
	return b, host, runner, sync.OnceFunc(func() { close(runner.release) })
}

func TestLastBinaryWaiterJoinsBeforeClassifyingCompletedFrame(t *testing.T) {
	for _, result := range []string{"success", "remote failure", "transport failure"} {
		for _, signal := range []string{"cancellation", "deadline"} {
			t.Run(result+"/"+signal, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					b, host, runner, release := newCompletedWaiter(t, result)
					var ctx context.Context
					var cancel context.CancelFunc
					if signal == "deadline" {
						ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					} else {
						ctx, cancel = context.WithCancel(context.Background())
					}
					defer func() { cancel(); release(); synctest.Wait() }()
					done := make(chan error, 1)
					go func() { done <- b.installBinary(ctx, host) }()
					work := <-runner.entered
					b.mu.Lock()
					job := b.installs[host]
					b.mu.Unlock()
					if signal == "deadline" {
						time.Sleep(time.Second)
					} else {
						cancel()
					}
					synctest.Wait()
					if work.Err() == nil || ctx.Err() == nil {
						t.Fatal("last waiter did not cancel the worker after its own signal")
					}
					if len(done) != 0 {
						t.Fatal("last waiter returned before joining the unfinished worker")
					}
					release()
					synctest.Wait()
					if len(done) != 1 {
						t.Fatal("joined waiter did not return after the worker completed")
					}
					got := <-done
					<-job.done
					if result == "success" {
						if got != nil || job.err != nil {
							t.Fatalf("positive activation frame must remain success after join: waiter=%v worker=%v", got, job.err)
						}
					} else if got == nil || job.err == nil {
						t.Fatalf("actual activation failure became success: waiter=%v worker=%v", got, job.err)
					} else if !errors.Is(got, ctx.Err()) {
						t.Fatalf("failed worker must retain the already selected subscriber signal: waiter=%v signal=%v worker=%v", got, ctx.Err(), job.err)
					}
				})
			})
		}
	}
}

func TestBinaryWaiterPreservesReturnedOperationError(t *testing.T) {
	for _, result := range []string{"remote failure", "transport failure"} {
		t.Run(result, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, host, runner, release := newCompletedWaiter(t, result)
				defer func() { release(); synctest.Wait() }()
				done := make(chan error, 1)
				go func() { done <- b.installBinary(context.Background(), host) }()
				<-runner.entered
				b.mu.Lock()
				job := b.installs[host]
				b.mu.Unlock()
				release()
				synctest.Wait()
				if len(done) != 1 {
					t.Fatal("worker did not finish")
				}
				if err := <-done; err == nil || !errors.Is(err, job.err) {
					t.Fatalf("returned operation error changed without any subscriber signal: waiter=%v worker=%v", err, job.err)
				}
			})
		})
	}
}

func TestCanceledNonLastBinaryWaiterLeavesPromptlyWithoutCancelingWorker(t *testing.T) {
	for _, result := range []string{"success", "remote failure"} {
		t.Run(result, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b, host, runner, release := newCompletedWaiter(t, result)
				ctx, cancel := context.WithCancel(context.Background())
				defer func() { cancel(); release(); synctest.Wait() }()
				first, second := make(chan error, 1), make(chan error, 1)
				go func() { first <- b.installBinary(ctx, host) }()
				work := <-runner.entered
				go func() { second <- b.installBinary(context.Background(), host) }()
				synctest.Wait()
				b.mu.Lock()
				refs := b.installs[host].refs
				b.mu.Unlock()
				if refs != 2 {
					t.Fatalf("second subscriber did not join the same transfer: refs=%d", refs)
				}
				cancel()
				synctest.Wait()
				if len(first) != 1 || len(second) != 0 || work.Err() != nil {
					t.Fatalf("canceled non-last waiter must leave promptly while another keeps working: first=%d second=%d worker=%v", len(first), len(second), work.Err())
				}
				if err := <-first; !errors.Is(err, context.Canceled) {
					t.Fatalf("non-last cancellation became success or another error: %v", err)
				}
				release()
				synctest.Wait()
				if len(second) != 1 {
					t.Fatal("remaining subscriber did not complete")
				}
				if err := <-second; (err == nil) != (result == "success") {
					t.Fatalf("remaining subscriber lost actual operation result: %v", err)
				}
			})
		})
	}
}
