package subagent

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"time"

	"dyna-agent/internal/sessionlife"
)

// Workers runs the engine's session workers, such as a transfer's
// coordinator, as goroutines on the engine's run context. Each one keeps its
// state in the session record after every step, so a worker that stops at
// any step resumes from that state when it runs again.
type Workers struct {
	ctx  context.Context
	logf func(string, ...any)

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// NewWorkers runs workers until ctx ends. logf reports a worker's failure.
func NewWorkers(ctx context.Context, logf func(string, ...any)) *Workers {
	if logf == nil {
		logf = log.Printf
	}
	return &Workers{ctx: ctx, logf: logf, running: map[string]bool{}}
}

// Go runs fn as the worker named key, unless that worker already runs. A
// panic ends the worker like an error does.
func (w *Workers) Go(key string, fn func(context.Context) error) {
	w.mu.Lock()
	if w.running[key] || w.ctx.Err() != nil {
		w.mu.Unlock()
		return
	}
	w.running[key] = true
	w.wg.Add(1)
	w.mu.Unlock()
	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			delete(w.running, key)
			w.mu.Unlock()
		}()
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
				}
			}()
			return fn(w.ctx)
		}()
		if err != nil && w.ctx.Err() == nil {
			w.logf("session worker %s stopped: %v", key, err)
		}
	}()
}

// Wait waits up to bound for the workers to leave after the run context
// ended, and returns the ones still running. A handoff wait can last two
// minutes, so engine stop never waits for one: the engine's next start
// resumes each worker from its stored state.
func (w *Workers) Wait(bound time.Duration) []string {
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(bound):
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	left := make([]string, 0, len(w.running))
	for key := range w.running {
		left = append(left, key)
	}
	sort.Strings(left)
	return left
}

// processWorkers run until this process ends, for a Manager outside the
// engine, such as a test's.
var processWorkers = sync.OnceValue(func() *Workers { return NewWorkers(context.Background(), nil) })

// workers is the engine's Workers, or this process's.
func (m *Manager) workers() *Workers {
	if m.Workers != nil {
		return m.Workers
	}
	return processWorkers()
}

// ResumeWorkersForEngine starts unfinished transfer and consolidation
// coordinators when the engine starts. Each resumes its stored state after
// an engine stop. One query per operation type finds them in session rows.
// A root seat handoff is left alone: its attach call has ended.
// A consolidation's transfers belong
// to its coordinator. It checks no caller, so it must never be an engine
// call (TestEngineMaintenanceIsNeverACall).
func (m *Manager) ResumeWorkersForEngine() error {
	ids, err := sessionlife.OpenWithPhase(m.store().root(), "transfer", activeTransferPhases...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		meta, err := m.store().ReadMeta(id)
		if err != nil || !m.CopyTime.IsZero() && !meta.Created.After(m.CopyTime) || !meta.Transfer.Active() || meta.Transfer.RootSeat || meta.Consolidating != "" {
			continue
		}
		launch := m.TransferLaunch
		if launch == nil {
			launch = goTransfer
		}
		if err := launch(m, id, meta.Transfer.ID); err != nil {
			return err
		}
	}
	ids, err = sessionlife.OpenWithPhase(m.store().root(), "consolidation", consolidationWorkerPhases...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		meta, err := m.store().ReadMeta(id)
		if err != nil {
			m.workers().logf("consolidation worker %s not resumed: %v", id, err)
			continue
		}
		if !m.CopyTime.IsZero() && !meta.Created.After(m.CopyTime) {
			continue
		}
		c := meta.Consolidation
		if c == nil || !slices.Contains(consolidationWorkerPhases, c.Phase) {
			continue
		}
		if err := m.launchConsolidation(*c); err != nil {
			return err
		}
	}
	return nil
}
