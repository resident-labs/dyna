package roomsvc

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// An idle tick skips the members scan. A session write that no wake covered
// and a session whose runner cannot be watched each make the next tick scan
// (requirement 16). A SessionsChanged wake scans every session at once,
// without a tick.
func TestMembersScanRunsOnlyWhenSomethingChanged(t *testing.T) {
	e := newEnv(t)
	rec := e.room("guard")
	info := e.start(rec, "worker", nil)
	w := newMemberWatch()
	records := []room.Record{rec}
	if !e.svc.membersChanged(w, records, true) {
		t.Fatal("the first scan was skipped")
	}
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("an idle tick scanned members")
	}
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	if !e.svc.membersChanged(w, records, false) {
		t.Fatal("a session write did not wake the scan")
	}
	e.svc.SessionsChanged()
	if _, all := e.svc.takeDirty(); !all {
		t.Fatal("a SessionsChanged wake did not ask for every session")
	}
	// A canonical turn write outside the engine moves the revision once.
	if _, err := e.mgr.Store.UpdateTurn(info.ID, info.Turns, func(turn *subagent.Turn) { turn.Prompt = "current write" }); err != nil {
		t.Fatal(err)
	}
	if !e.svc.membersChanged(w, records, false) {
		t.Fatal("a turn write did not wake the scan")
	}
	if turn, err := e.mgr.Store.ReadTurn(info.ID, info.Turns); err != nil || turn.Prompt != "current write" {
		t.Fatalf("the turn reads %q, %v; want the canonical write", turn.Prompt, err)
	}
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("an idle tick scanned members after one turn write")
	}
	w.runners = newRunnerWatch(func() {}, nil)
	w.runners.unwatched[info.ID] = true
	for range 2 {
		if !e.svc.membersChanged(w, records, false) {
			t.Fatal("a session whose runner cannot be watched was not scanned every tick")
		}
	}
	delete(w.runners.unwatched, info.ID)
	e.svc.membersChanged(w, records, false)
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("an idle tick scanned members after the unwatched runner went")
	}
}

// startRunner starts a real child for the pidfd and exit checks. Executable
// classification is injected separately, so this fixture never copies a binary.
func startRunner(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func runningInfo(id string, pid int) subagent.Info {
	info := subagent.Info{Status: subagent.StatusRunning}
	info.ID, info.Pid = id, pid
	return info
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(entries)
}

// The runner's exit wakes the scan. A runner that cannot be verified, or is
// already gone, wakes it at once and keeps its session on every-tick scans.
// Closing the watch ends every watcher within the stop budget and leaks no
// handle.
func TestRunnerWatchWakesOnExitAndFallsBack(t *testing.T) {
	launchedAs := func(id string, format int) func(callerid.Process) (callerid.Launched, bool, error) {
		return func(callerid.Process) (callerid.Launched, bool, error) {
			return callerid.Launched{Identity: callerid.Identity{Kind: callerid.Session, Session: id}, Format: format}, true, nil
		}
	}
	recordedAs := func(id string) func(callerid.Process) (callerid.Launched, bool, error) {
		return launchedAs(id, callerid.StoreFormat)
	}
	t.Run("exit wakes", func(t *testing.T) {
		cmd := startRunner(t)
		woke := make(chan struct{}, 1)
		w := newRunnerWatch(func() { woke <- struct{}{} }, recordedAs("ses_a"))
		defer w.close()
		w.follow([]subagent.Info{runningInfo("ses_a", cmd.Process.Pid)})
		if w.needsFullScans() {
			t.Fatal("a recorded runner of this binary fell back")
		}
		_ = cmd.Process.Kill()
		select {
		case <-woke:
		case <-time.After(5 * time.Second):
			t.Fatal("the runner's exit did not wake the scan")
		}
	})
	t.Run("unrecorded start time falls back", func(t *testing.T) {
		cmd := startRunner(t)
		woke := make(chan struct{}, 1)
		w := newRunnerWatch(func() { woke <- struct{}{} }, func(callerid.Process) (callerid.Launched, bool, error) {
			return callerid.Launched{}, false, nil
		})
		defer w.close()
		w.follow([]subagent.Info{runningInfo("ses_a", cmd.Process.Pid)})
		if !w.needsFullScans() || len(woke) != 1 {
			t.Fatalf("full scans %v, wakes %d; want a fallback with one wake", w.needsFullScans(), len(woke))
		}
	})
	t.Run("gone runner falls back", func(t *testing.T) {
		cmd := startRunner(t)
		pid := cmd.Process.Pid
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		woke := make(chan struct{}, 1)
		w := newRunnerWatch(func() { woke <- struct{}{} }, recordedAs("ses_a"))
		defer w.close()
		w.follow([]subagent.Info{runningInfo("ses_a", pid)})
		if !w.needsFullScans() || len(woke) != 1 {
			t.Fatalf("full scans %v, wakes %d; want a fallback with one wake", w.needsFullScans(), len(woke))
		}
	})
	t.Run("an earlier release's launch record is still watched", func(t *testing.T) {
		cmd := startRunner(t)
		w := newRunnerWatch(func() {}, launchedAs("ses_a", 0))
		defer w.close()
		w.follow([]subagent.Info{runningInfo("ses_a", cmd.Process.Pid)})
		if w.watched["ses_a"] != cmd.Process.Pid || w.needsFullScans() {
			t.Fatalf("watched %v, full scans %v; want its runner watched without full scans", w.watched, w.needsFullScans())
		}
	})

	t.Run("close ends watchers without leaks", func(t *testing.T) {
		cmd := startRunner(t)
		before := openFDs(t)
		w := newRunnerWatch(func() {}, recordedAs("ses_a"))
		w.follow([]subagent.Info{runningInfo("ses_a", cmd.Process.Pid)})
		closed := make(chan struct{})
		start := time.Now()
		go func() { w.close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("close did not end the watcher")
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("close took %v; the stop budget is 1 s", took)
		}
		if after := openFDs(t); after > before {
			t.Fatalf("open handles %d after close, %d before", after, before)
		}
	})
}

// One engine write to one session gives one members event for it, with no
// tick. The fallback tick can scan the full list once, without repeating
// that event; later idle ticks skip the scan. A burst of writes gives one
// targeted scan of every written session. A write that no wake covered, as
// from a CLI process, still reaches the fallback (requirement 16).
func TestSessionWakesScanOnlyTheWrittenSessions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("bus")
	a := e.start(rec, "a", nil)
	b := e.start(rec, "b", nil)
	c := e.start(rec, "c", nil)
	records := []room.Record{rec}
	w := newMemberWatch()
	if !e.svc.membersChanged(w, records, true) {
		t.Fatal("the first scan was skipped")
	}
	sessions, err := w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.scanMembers(sessions, records, w, nil, true, func(Event) {})
	var scans []int
	e.svc.onMemberScan = func(n int) { scans = append(scans, n) }
	var events []Event
	collect := func(ev Event) {
		if ev.Kind == "members" {
			events = append(events, ev)
		}
	}
	write := func(info subagent.Info, profile string) {
		t.Helper()
		if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Profile = profile }); err != nil {
			t.Fatal(err)
		}
	}
	write(a, "one")
	e.svc.SessionChanged(a.ID) // what the engine's publish does after the commit
	e.svc.scanDirtyMembers(w, collect)
	if len(scans) != 1 || scans[0] != 1 || len(events) != 1 || events[0].Change.ID != a.ID || events[0].Change.Op != "upsert" {
		t.Fatalf("one write gave scans %v and events %+v; want one scan of one session and one upsert for %s", scans, events, a.ID)
	}
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("the fallback tick repeated a fully covered targeted scan")
	}
	sessions, err = w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	e.svc.scanMembers(sessions, records, w, nil, false, collect)
	if len(events) != 0 {
		t.Fatalf("fallback repeated the targeted event: %+v", events)
	}
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("an idle tick scanned members after the fallback caught up")
	}
	scans, events = nil, nil
	for _, info := range []subagent.Info{a, b, c} {
		write(info, "burst")
		e.svc.SessionChanged(info.ID)
	}
	e.svc.scanDirtyMembers(w, collect)
	ids := map[string]bool{}
	for _, ev := range events {
		ids[ev.Change.ID] = true
	}
	if len(scans) != 1 || scans[0] != 3 || len(ids) != 3 || !ids[a.ID] || !ids[b.ID] || !ids[c.ID] {
		t.Fatalf("three writes gave scans %v covering %v; want one scan of the three sessions", scans, ids)
	}
	write(b, "outside")
	if !e.svc.membersChanged(w, records, false) {
		t.Fatal("a write that no wake covered did not make the tick scan")
	}
}

// A targeted wake cannot acknowledge writes to sessions that it did not read.
// Keep the fallback tick able to find a write from an earlier-release runner.
func TestTargetedMemberScanPreservesUnwokenRevision(t *testing.T) {
	e := newEnv(t)
	rec := e.room("mixed-writes")
	a := e.start(rec, "a", nil)
	b := e.start(rec, "b", nil)
	records := []room.Record{rec}
	w := newMemberWatch()
	if !e.svc.membersChanged(w, records, true) {
		t.Fatal("the baseline scan was skipped")
	}
	sessions, err := w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	e.svc.scanMembers(sessions, records, w, nil, true, func(Event) {})
	for _, info := range []subagent.Info{b, a} {
		if _, err := e.mgr.Store.UpdateMeta(info.ID, func(meta *subagent.Meta) {
			meta.Profile = "changed"
		}); err != nil {
			t.Fatal(err)
		}
	}
	var events []Event
	collect := func(event Event) {
		if event.Kind == "members" {
			events = append(events, event)
		}
	}
	e.svc.SessionChanged(a.ID)
	e.svc.scanDirtyMembers(w, collect)
	if len(events) != 1 || events[0].Change.ID != a.ID {
		t.Fatalf("targeted scan emitted %+v; want only %s", events, a.ID)
	}
	if !e.svc.membersChanged(w, records, false) {
		t.Fatal("the targeted scan hid the unwoken session revision from the fallback tick")
	}
	sessions, err = w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	e.svc.scanMembers(sessions, records, w, nil, false, collect)
	if len(events) != 1 || events[0].Change.ID != b.ID || events[0].Change.Op != "upsert" {
		t.Fatalf("fallback scan emitted %+v; want one upsert for %s", events, b.ID)
	}
	if e.svc.membersChanged(w, records, false) {
		t.Fatal("an idle tick scanned members after the fallback caught up")
	}
}
