package roomsvc

import (
	"os"
	"sync"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/subagent"
)

// runnerWatch follows the turn runners of running sessions (requirement 16).
// A runner releases its turn lock after its last row write, so its exit
// changes a session's status with no write. Each runner of this release is
// watched by a pidfd, and its exit wakes the members scan.
// A runner that cannot be verified or watched wakes the next scan and
// stays on full scans until it stops running. This also covers systems
// where opening a pidfd fails.
type runnerWatch struct {
	wake     func()
	recorded func(callerid.Process) (callerid.Launched, bool, error)
	open     func(int) (runnerProcess, error)
	// exited, when set, runs after a watched runner exits, before the wake.
	exited            func(session string)
	dataDir           string
	coordinatorExited func(run string)
	coordinators      map[callerid.Process]struct{}

	mu        sync.Mutex
	watched   map[string]int  // session ID -> runner PID with a live watcher
	unwatched map[string]bool // sessions whose runner cannot be watched
	closed    bool
	done      sync.WaitGroup
	quit      quitter
}

func newRunnerWatch(wake func(), recorded func(callerid.Process) (callerid.Launched, bool, error)) *runnerWatch {
	return &runnerWatch{
		wake: wake, recorded: recorded,
		open:         func(pid int) (runnerProcess, error) { return openRunner(pid) },
		watched:      map[string]int{},
		coordinators: map[callerid.Process]struct{}{},
		unwatched:    map[string]bool{},
	}
}

type runnerProcess interface {
	wait(*quitter) bool
	close()
}

// follow watches the runner of every running session it does not watch yet,
// and forgets sessions that no longer run. A nil watch watches nothing.
func (w *runnerWatch) follow(sessions []subagent.Info) {
	if w == nil {
		return
	}
	running := map[string]bool{}
	for _, info := range sessions {
		if info.Status != subagent.StatusRunning && info.Status != subagent.StatusQueued || info.Pid <= 0 {
			continue
		}
		running[info.ID] = true
		w.mu.Lock()
		watched := w.watched[info.ID] == info.Pid
		w.mu.Unlock()
		if !watched {
			w.watch(info)
		}
	}
	w.mu.Lock()
	for id := range w.unwatched {
		if !running[id] {
			delete(w.unwatched, id)
		}
	}
	w.mu.Unlock()
}

// watch opens a watcher on info's runner. A runner it cannot verify keeps
// its session on every-tick scans, and wakes the next scan in case it is
// already gone.
func (w *runnerWatch) watch(info subagent.Info) {
	fallback := func() {
		w.mu.Lock()
		w.unwatched[info.ID] = true
		w.mu.Unlock()
		w.wake()
	}
	h, err := w.open(info.Pid)
	if err != nil {
		fallback()
		return
	}
	// The handle pins the PID, so the start time read now names the process
	// the handle watches.
	var launch callerid.Launched
	proc, err := callerid.ProcessOf(info.Pid)
	if err == nil {
		var ok bool
		launch, ok, err = w.recorded(proc)
		if err == nil && (!ok || launch.Session != info.ID) {
			err = os.ErrNotExist
		}
	}
	if err != nil {
		h.close()
		fallback()
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		h.close()
		return
	}
	w.watched[info.ID] = info.Pid
	w.done.Add(1)
	w.mu.Unlock()
	w.wait(h, func(exited bool) {
		w.mu.Lock()
		if w.watched[info.ID] == info.Pid {
			delete(w.watched, info.ID)
		}
		w.mu.Unlock()
		if exited {
			if w.exited != nil {
				w.exited(info.ID)
			}
			w.wake()
		}
	})
}

// wait is the one pidfd lifecycle for session and workflow coordinators.
// The caller adds to done under mu after checking closed.
func (w *runnerWatch) wait(h runnerProcess, after func(bool)) {
	go func() {
		defer w.done.Done()
		exited := h.wait(&w.quit)
		h.close()
		after(exited)
	}()
}

// needsFullScans reports whether a running session's runner cannot be
// watched, so every tick must scan.
func (w *runnerWatch) needsFullScans() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.unwatched) > 0
}

// close ends every watcher and waits for them, so no handle outlives Watch.
func (w *runnerWatch) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.quit.signal()
	w.done.Wait()
	w.quit.close()
}
