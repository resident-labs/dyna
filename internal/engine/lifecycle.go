package engine

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/sysload"
)

func begin(ctx context.Context, rooms *roomsvc.Service, accounts *service.Service, load *sysload.Sampler, readOnly bool, signals *workerSignals, t *timers, events *hub.Hub, emit func(roomsvc.Event)) <-chan struct{} {
	watch := ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !readOnly {
			n, err := rooms.Notify.Sessions.Store.ImportLegacyRecords()
			if n > 0 {
				log.Printf("imported %d session records into dyna.db", n)
			}
			if err != nil {
				log.Printf("session records not imported: %v", err)
			}
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
				refreshAccounts(watch, accounts.RefreshAccounts, t, events)
			}
		}()
		versionsDone := make(chan struct{})
		go func() {
			defer close(versionsDone)
			if !readOnly {
				harnessruntime.RunVersionChecks(watch)
			}
		}()
		loadDone := make(chan struct{})
		go func() { defer close(loadDone); load.Run(watch) }()
		usageDone := make(chan struct{})
		go func() {
			defer close(usageDone)
			if !readOnly {
				rooms.Notify.Sessions.Store.RunUsageRepair(watch, func(err error) { log.Print(err) })
			}
		}()
		recoveryDone := make(chan struct{})
		if !readOnly {
			if err := rooms.Notify.Sessions.ResumeWorkersForEngine(); err != nil {
				log.Printf("session workers not resumed: %v", err)
			}
			go func() { defer close(recoveryDone); rooms.Notify.Sessions.RecoverRoomSessionResumes(watch) }()
		} else {
			close(recoveryDone)
		}
		workersDone := make(chan struct{})
		if !readOnly {
			go func() { defer close(workersDone); runWorkers(watch, rooms, signals, t) }()
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
		<-versionsDone
		<-usageDone
		<-loadDone
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
