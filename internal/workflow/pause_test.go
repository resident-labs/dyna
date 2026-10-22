package workflow

import (
	"context"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
)

func TestPausedWorkflowResumesOrCancelsBeforeWorkerLaunch(t *testing.T) {
	for _, resume := range []bool{true, false} {
		name := "cancel"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			storetest.DataDir(t)
			ctx, cancel := context.WithCancel(t.Context())
			var paused atomic.Bool
			paused.Store(true)
			checked := make(chan struct{}, 2)
			thirdCheck := make(chan struct{})
			checks := 0
			running := make(chan struct{}, 1)
			done := make(chan error, 1)
			finished := make(chan struct{})
			workDir := t.TempDir()
			t.Cleanup(func() {
				cancel()
				<-finished
			})
			go func() {
				defer close(finished)
				_, err := Execute(ctx, Options{
					ScriptSrc: `return await agent("SLEEP:0\nRESPOND: ok");`,
					Store:     mockWorkflowStore(0, 0), WorkDir: workDir,
					Paused: func() bool {
						value := paused.Load()
						if value {
							checks++
							if checks <= 2 {
								select {
								case checked <- struct{}{}:
								default:
								}
							}
							if checks == 3 {
								close(thirdCheck)
							}
						}
						return value
					},
					OnEvent: func(e runstore.Event) {
						if e.T == "agent_run" {
							running <- struct{}{}
						}
					},
				})
				done <- err
			}()
			// A second true check proves the worker stayed in the pause loop.
			// Wait for callbacks, launch or completion rather than elapsed time.
			for range 2 {
				select {
				case <-checked:
				case <-running:
					t.Fatal("worker launched while paused")
				case err := <-done:
					t.Fatalf("paused workflow ended: %v", err)
				}
			}
			// Stop receiving notifications, then exercise another true check.
			// A slow receiver must never hold Execute inside its Paused callback.
			select {
			case <-thirdCheck:
			case <-running:
				t.Fatal("worker launched while paused")
			case err := <-done:
				t.Fatalf("paused workflow ended: %v", err)
			}
			if resume {
				paused.Store(false)
			} else {
				cancel()
			}
			err := <-done
			if resume && err != nil {
				t.Fatalf("resume: %v", err)
			}
			if !resume && err == nil {
				t.Fatal("canceled workflow succeeded")
			}
			select {
			case <-running:
				if !resume {
					t.Fatal("canceled worker launched")
				}
			default:
				if resume {
					t.Fatal("resumed worker did not launch")
				}
			}
		})
	}
}

func TestCanceledWorkflowDoesNotLaunchAfterPauseCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var launched atomic.Bool
	_, err := Execute(ctx, Options{
		ScriptSrc: `return await agent("RESPOND: ok");`,
		Store:     mockWorkflowStore(0, 0), WorkDir: t.TempDir(),
		Paused: func() bool {
			cancel()
			return false
		},
		OnEvent: func(e runstore.Event) {
			if e.T == "agent_run" {
				launched.Store(true)
			}
		},
	})
	if err == nil || launched.Load() {
		t.Fatalf("canceled workflow: error=%v, launched=%v", err, launched.Load())
	}
}
