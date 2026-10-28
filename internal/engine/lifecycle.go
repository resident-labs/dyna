package engine

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/admission"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	storepkg "dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
)

func begin(ctx context.Context, rooms *roomsvc.Service, accounts *service.Service, load *sysload.Sampler, readOnly bool, signals *workerSignals, t *timers, events *hub.Hub, hookService hooks.Service, started time.Time, jobs *jobs.Service, emit func(roomsvc.Event)) <-chan struct{} {
	watch := ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !readOnly {
			if n, err := runstore.ReconcileLostRuns(); err != nil {
				log.Printf("lost workflow runs not reconciled: %v", err)
			} else if n > 0 {
				log.Printf("marked %d lost workflow runs as failed", n)
			}
		}
		refreshDone := make(chan struct{})
		go func() {
			defer close(refreshDone)
			if !readOnly {
				// Renewal stays on the account timer, at the exact expiry deadline:
				// a late renewal logs the person out. Usage probes run as the
				// account-usage built-in hook, and harness versions as the
				// harness-versions hook.
				refreshAccounts(watch, accounts.RefreshCredentials, t, events)
			}
		}()
		loadDone := make(chan struct{})
		go func() { defer close(loadDone); load.Run(watch) }()
		// The admission watcher only records; no turn calls it.
		admissionDone := make(chan struct{})
		go func() {
			defer close(admissionDone)
			if readOnly {
				return
			}
			ticker := time.NewTicker(admission.SampleEvery)
			defer ticker.Stop()
			admissionObserver(rooms.Notify.Sessions, load, storepkg.DataDir()).Run(watch, ticker.C)
		}()
		usageDone := make(chan struct{})
		go func() {
			defer close(usageDone)
			if !readOnly {
				rooms.Notify.Sessions.Store.RunUsageRepair(watch, func(err error) { log.Print(err) })
			}
		}()
		recoveryDone := make(chan struct{})
		if !readOnly {
			if err := rooms.Notify.Sessions.RetireGoalKeepers(watch); err != nil {
				log.Printf("goal keeper retirement: %v", err)
			}
			if err := rooms.Notify.Sessions.ResumeWorkersForEngine(); err != nil {
				log.Printf("session workers not resumed: %v", err)
			}
			go func() { defer close(recoveryDone); rooms.Notify.Sessions.RecoverRoomSessionResumes(watch) }()
		} else {
			close(recoveryDone)
		}
		workersDone := make(chan struct{})
		if !readOnly {
			go func() {
				defer close(workersDone)
				runWorkers(watch, rooms, signals, t, load, events, hookService, started, jobs)
			}()
		} else {
			close(workersDone)
		}
		// Held root input from before this start gets its timed flush too;
		// startRootFlush recovers it after the watch's first scan, so no post
		// can fall between that scan's baseline and the recovery read.
		var flush *rootFlush
		if !readOnly {
			flush, _ = startRootFlush(rooms, t)
		}
		rooms.Watch(watch, 500*time.Millisecond, func(ev roomsvc.Event) {
			if !readOnly {
				signals.wake(ev)
				flush.events(ev)
			}
			notifyQuestions(ev)
			emit(ev)
		})
		<-workersDone
		if !readOnly {
			stopSessionWorkers(rooms.Notify.Sessions.Workers, log.Printf)
		}
		<-recoveryDone
		<-refreshDone
		<-usageDone
		<-loadDone
		<-admissionDone
	}()
	return done
}

func newRuntime(ctx context.Context) *harnessruntime.Runtime {
	runtime := harnessruntime.New()
	runtime.SetLifetime(ctx)
	return runtime
}

// refreshAccounts shares one timer for credential renewal and inactive usage.
// Each pass returns the earliest deadline for the next pass. A committed
// change to the account registry runs the next pass at once. A subscriber the
// hub dropped as too slow resubscribes and runs a pass, since it may have
// missed a change.
func refreshAccounts(ctx context.Context, pass func(context.Context, time.Time) time.Time, t *timers, events *hub.Hub) {
	wake := make(chan struct{}, 1)
	changes, _, unsubscribe := events.Subscribe()
	defer func() { unsubscribe() }()
	for {
		next := pass(ctx, t.now())
		t.at(timerAccounts, next, func() { wakeWorker(wake) })
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				waiting = false
			case ev, open := <-changes:
				if !open {
					changes, _, unsubscribe = events.Subscribe()
				}
				waiting = open && ev.Topic != accountstore.Topic
			}
		}
	}
}

// sessionWorkersStop bounds how long engine stop waits for session workers.
// A transfer can wait two minutes for a handoff; it leaves at its next step,
// and the next engine start resumes any worker still running.
const sessionWorkersStop = 3 * time.Second

func stopSessionWorkers(w *subagent.Workers, logf func(string, ...any)) {
	if w == nil {
		return
	}
	if left := w.Wait(sessionWorkersStop); len(left) > 0 {
		logf("engine stop left %d session workers running, for the next start to resume: %v", len(left), left)
	}
}
