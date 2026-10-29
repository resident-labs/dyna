package roomsvc

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/terminalsvc"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

func TestOldShellJobUpdateCarriesItemOutsideTheHead(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-events")
	shellStore := terminalsvc.ShellStore(e.svc.sessions().Root)
	job, err := shellStore.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner", Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	exit := &shells.Exit{Ended: time.Now().Add(-48 * time.Hour)}
	if err := shellStore.Save(job, exit); err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) { t.Fatal("initial shell event") })
	job.Name = "After"
	if err := shellStore.Save(job, exit); err != nil {
		t.Fatal(err)
	}
	var events []Event
	e.svc.scanShellJobs(jobs, false, func(event Event) { events = append(events, event) })
	if len(events) != 1 {
		t.Fatalf("shell job change emitted %d events, want 1", len(events))
	}
	data, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Kind, RoomID string
		Change       *struct {
			Op, ID string
			InHead *bool
			Item   terminalsvc.ShellJob
		}
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Kind != "jobs" || wire.RoomID != rec.ID || wire.Change == nil || wire.Change.Op != "upsert" || wire.Change.ID != job.ID || wire.Change.InHead == nil || *wire.Change.InHead || wire.Change.Item.Name != "After" || wire.Change.Item.ID != job.ID || wire.Change.Item.Owner != "owner" || wire.Change.Item.Ended == nil {
		t.Fatalf("shell job event lost its canonical history item: %s", data)
	}
	e.svc.scanShellJobs(jobs, false, func(Event) { t.Fatal("unchanged shell event") })
}

func TestSessionViewEmbedsTheJobOfEachShellCall(t *testing.T) {
	e := newEnv(t)
	lead := e.start(e.room("shell-blocks"), "lead", nil)
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	done, err := store.Create(shells.StartOptions{Name: "Build", Cmd: []string{"bash", "-c", "make"}, Cwd: t.TempDir(), RoomID: lead.RoomID, Owner: lead.ID})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.Create(shells.StartOptions{Name: "Serve", Cmd: []string{"bash", "-c", "make serve"}, Cwd: t.TempDir(), RoomID: lead.RoomID, Owner: lead.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Another session's identical job must never be taken as ours.
	if _, err := store.Create(shells.StartOptions{Name: "Serve", Cmd: []string{"bash", "-c", "make serve"}, Cwd: t.TempDir(), RoomID: lead.RoomID, Owner: "another-session"}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(done.ID)
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(done, &shells.Exit{Code: 2, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	call := func(name, input, output string) workeroutput.Event {
		return workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: name, State: "completed", Input: input, Output: output}
	}
	items, _ := transcript.FromEvents([]workeroutput.Event{
		call("mcp__dyna__shell", `{"command":"make","description":"Build"}`, "Job "+done.ID+". Exit code 2.\none\ntwo\n"),
		call("mcp__dyna__shell_output", `{"job_id":"`+running.ID+`"}`, ""),
		// Still running: no result yet, so it is matched by command and description.
		{V: workeroutput.SchemaVersion, Kind: "tool", Name: "mcp__dyna__shell", State: "started", Input: `{"command":"make serve","description":"Serve"}`},
		call("bash", `{"command":"ls"}`, "a.go"),
	})
	view, err := e.svc.sessionView(transcript.Session{Info: lead, Turns: []transcript.Turn{{N: 1, Items: items}}})
	if err != nil {
		t.Fatal(err)
	}
	got := view.Turns[0].Items
	if len(got) != 4 {
		t.Fatalf("items = %+v", got)
	}
	if got[0].JobID != done.ID || got[0].Job == nil || got[0].Job.Code != 2 || got[0].Job.Tail != "one\ntwo" || got[0].Job.OutputBytes != 8 {
		t.Fatalf("finished run = %+v %+v", got[0], got[0].Job)
	}
	if got[1].JobOp != "output" || got[1].JobID != running.ID || got[1].Job != nil {
		t.Fatalf("output call = %+v", got[1])
	}
	if got[2].JobID != running.ID || got[2].Job == nil || got[2].Job.State == "" {
		t.Fatalf("running run = %+v", got[2])
	}
	if got[3].JobID != "" || got[3].Job != nil {
		t.Fatalf("a harness's own shell got a job: %+v", got[3])
	}
}

func TestJobRowCarriesTheTailWhileOpen(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-tail")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(job.ID)
	if err := os.WriteFile(path, []byte("a\r\nb\nc\nd\ne\nf\ng\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	in, err := store.Info(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row := terminalsvc.ShellJobRow(store, in); row.Tail != "b\nc\nd\ne\nf\ng" || row.OutputBytes != 16 {
		t.Fatalf("open row = %+v", row)
	}
	// An old finished job carries no tail, so a list of many jobs reads no logs.
	if err := store.Save(job, &shells.Exit{Ended: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if in, err = store.Info(job.ID); err != nil {
		t.Fatal(err)
	}
	if row := terminalsvc.ShellJobRow(store, in); row.Tail != "" || row.OutputBytes != 0 {
		t.Fatalf("old row = %+v", row)
	}
	if block := terminalsvc.ShellJobBlock(store, in); block.Tail != "b\nc\nd\ne\nf\ng" {
		t.Fatal("a transcript block has no tail")
	}
}

func TestSessionViewLinksGuessesWithinTheirTurnAndKeepsTheCacheIntact(t *testing.T) {
	e := newEnv(t)
	lead := e.start(e.room("shell-turns"), "lead", nil)
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	create := func(started time.Time) shells.Meta {
		t.Helper()
		job, err := store.Create(shells.StartOptions{Name: "Build", Cmd: []string{"bash", "-c", "make"}, Cwd: t.TempDir(), RoomID: lead.RoomID, Owner: lead.ID})
		if err != nil {
			t.Fatal(err)
		}
		job.Started = started
		if err := store.Save(job, &shells.Exit{Ended: started.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		return job
	}
	now := time.Now()
	// Two turns that touch: the job at the first turn's end is its own.
	early, late := create(now.Add(-time.Minute-time.Second)), create(now)
	call := workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "mcp__dyna__shell", State: "started", Input: `{"command":"make","description":"Build"}`}
	items, _ := transcript.FromEvents([]workeroutput.Event{call})
	turns := []transcript.Turn{
		{N: 1, Started: now.Add(-time.Hour), Ended: now.Add(-time.Minute), Items: items},
		{N: 2, Started: now.Add(-time.Minute + time.Second), Items: items},
	}
	view, err := e.svc.sessionView(transcript.Session{Info: lead, Turns: turns})
	if err != nil {
		t.Fatal(err)
	}
	// Each running call gets the job that started in its own turn.
	if view.Turns[0].Items[0].JobID != early.ID || view.Turns[1].Items[0].JobID != late.ID {
		t.Fatalf("jobs = %q, %q; want %q, %q", view.Turns[0].Items[0].JobID, view.Turns[1].Items[0].JobID, early.ID, late.ID)
	}
	// A page that holds only the later turn does not show the earlier job.
	view, err = e.svc.sessionView(transcript.Session{Info: lead, Turns: turns[1:]})
	if err != nil || view.Turns[0].Items[0].JobID != late.ID {
		t.Fatalf("single turn = %+v, %v", view.Turns, err)
	}
	if items[0].Tool.JobID != "" {
		t.Fatal("linking changed the cached items")
	}
}

func TestJobBlockSaysWhenItsLogWasPruned(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-pruned")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(job, &shells.Exit{Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	in, err := store.Info(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if block := terminalsvc.ShellJobBlock(store, in); block.LogPruned {
		t.Fatalf("a job with a log is pruned: %+v", block)
	}
	path, _ := store.OutputPath(job.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if block := terminalsvc.ShellJobBlock(store, in); !block.LogPruned || block.Tail != "" {
		t.Fatalf("pruned block = %+v", block)
	}
}

// A finished job leaves the first page after 24 hours with no write. The
// watcher must still rescan then and report it out of the head.
func TestShellJobLeavingTheFirstPageByAgeIsReported(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-expiry")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	now := time.Now()
	oldClock := terminalsvc.Clock
	terminalsvc.Clock = func() time.Time { return now }
	t.Cleanup(func() { terminalsvc.Clock = oldClock })
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(job, &shells.Exit{Ended: now.Add(-24*time.Hour + time.Hour)}); err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	e.svc.scanShellJobs(jobs, false, func(Event) { t.Fatal("event without a change") })
	now = now.Add(2 * time.Hour)
	var events []Event
	e.svc.scanShellJobs(jobs, false, func(event Event) { events = append(events, event) })
	if len(events) != 1 || events[0].Change == nil || events[0].Change.ID != job.ID || events[0].Change.InHead == nil || *events[0].Change.InHead {
		t.Fatalf("job that aged out of the first page: events %+v, want one update with InHead false", events)
	}
}

// A supervisor that dies without recording an exit changes the job's state
// with no database write; the watcher reports it as lost.
func TestShellJobWhoseSupervisorDiedIsReportedLost(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-lost")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	supervisor := exec.Command("sleep", "60")
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { supervisor.Process.Kill(); supervisor.Wait() })
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	job.Supervisor = supervisor.Process.Pid
	if err := store.Save(job, nil); err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	if err := supervisor.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	supervisor.Wait()
	var events []Event
	e.svc.scanShellJobs(jobs, false, func(event Event) { events = append(events, event) })
	if len(events) != 1 || events[0].Change == nil {
		t.Fatalf("dead supervisor: events %+v, want one update", events)
	}
	var item terminalsvc.ShellJob
	if err := json.Unmarshal(events[0].Change.Item, &item); err != nil || item.State != "lost" {
		t.Fatalf("dead supervisor reported as %q (%v), want lost", item.State, err)
	}
}

// A live job's row changes with no database write: its tail grows while it
// runs, and it drops the tail a minute after it ends. The watcher reports
// each change once and nothing in between.
func TestShellJobTailChangesWithoutAWriteAreReported(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-live")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	now := time.Now()
	oldClock := terminalsvc.Clock
	terminalsvc.Clock = func() time.Time { return now }
	t.Cleanup(func() { terminalsvc.Clock = oldClock })
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	scan := func() []terminalsvc.ShellJob {
		t.Helper()
		var rows []terminalsvc.ShellJob
		e.svc.scanShellJobs(jobs, false, func(event Event) {
			var row terminalsvc.ShellJob
			if event.Change == nil || json.Unmarshal(event.Change.Item, &row) != nil || row.ID != job.ID {
				t.Fatalf("unexpected event %+v", event)
			}
			rows = append(rows, row)
		})
		return rows
	}
	if rows := scan(); len(rows) != 0 {
		t.Fatalf("events without a change: %+v", rows)
	}
	path, _ := store.OutputPath(job.ID)
	if err := os.WriteFile(path, []byte("progress\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store.Notify(job.ID)
	if rows := scan(); len(rows) != 1 || rows[0].Tail != "progress" {
		t.Fatalf("output growth: %+v, want one row with the tail", rows)
	}
	if err := store.Save(job, &shells.Exit{Code: 1, Ended: now}); err != nil {
		t.Fatal(err)
	}
	if rows := scan(); len(rows) != 1 || rows[0].Ended == nil || rows[0].Tail != "progress" {
		t.Fatalf("exit: %+v, want one ended row that keeps its tail", rows)
	}
	if rows := scan(); len(rows) != 0 {
		t.Fatalf("events without a change after the exit: %+v", rows)
	}
	now = now.Add(time.Minute)
	if rows := scan(); len(rows) != 1 || rows[0].Tail != "" {
		t.Fatalf("a minute after the exit: %+v, want one row without its tail", rows)
	}
	if rows := scan(); len(rows) != 0 {
		t.Fatalf("events after the tail dropped: %+v", rows)
	}
}

// A running job's output grows with no write; its supervisor sends an event.
// The watcher reads the job only on that event, never its folder on a scan.
func TestRunningShellJobOutputIsReadOnItsEventOnly(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-events-only")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	supervisor := exec.Command("sleep", "60")
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { supervisor.Process.Kill(); supervisor.Wait() })
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	job.Supervisor = supervisor.Process.Pid
	if err := store.Save(job, nil); err != nil {
		t.Fatal(err)
	}
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	path, _ := store.OutputPath(job.ID)
	if err := os.WriteFile(path, []byte("progress\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e.svc.scanShellJobs(jobs, false, func(event Event) { t.Fatalf("a scan read the job's folder with no event: %+v", event) })
	store.Notify(job.ID)
	var rows []terminalsvc.ShellJob
	e.svc.scanShellJobs(jobs, false, func(event Event) {
		var row terminalsvc.ShellJob
		if event.Change == nil || json.Unmarshal(event.Change.Item, &row) != nil {
			t.Fatalf("unexpected event %+v", event)
		}
		rows = append(rows, row)
	})
	if len(rows) != 1 || rows[0].State != "running" || rows[0].Tail != "progress" {
		t.Fatalf("after the event: %+v, want one running row with the tail", rows)
	}
}

// A room's first page reads only its open jobs and its closed jobs from the
// last 24 hours. A job closed before that window never reaches the page build,
// so a dirty room does not rebuild over every job it has ever run.
func TestDirtyRoomPageSkipsClosedJobsOutsideTheWindow(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shell-window")
	store := terminalsvc.ShellStore(e.svc.sessions().Root)
	now := time.Now()
	oldClock := terminalsvc.Clock
	terminalsvc.Clock = func() time.Time { return now }
	t.Cleanup(func() { terminalsvc.Clock = oldClock })
	closed := func(ended time.Time) {
		t.Helper()
		job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(job, &shells.Exit{Ended: ended}); err != nil {
			t.Fatal(err)
		}
	}
	closed(now.Add(-time.Hour))
	closed(now.Add(-48 * time.Hour))
	jobs := newShellJobWatch()
	t.Cleanup(jobs.close)
	e.svc.scanShellJobs(jobs, true, func(Event) {})
	var built []listpage.Meta
	oldPage := pageShellJobs
	pageShellJobs = func(items []listpage.Meta) (listpage.Page[listpage.Meta], error) {
		built = items
		return oldPage(items)
	}
	t.Cleanup(func() { pageShellJobs = oldPage })
	open, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	e.svc.scanShellJobs(jobs, false, func(event Event) { events = append(events, event) })
	if len(built) != 2 {
		t.Fatalf("page built over %d jobs, want the open job and the recent closed job", len(built))
	}
	for _, m := range built {
		if m.ID != open.ID && m.At.Before(now.Add(-listpage.Recent)) {
			t.Fatalf("page built over job %s closed before the 24 hour window", m.ID)
		}
	}
	if len(events) != 1 || events[0].Change == nil || events[0].Change.ID != open.ID || events[0].Change.InHead == nil || !*events[0].Change.InHead {
		t.Fatalf("events = %+v, want one head upsert for the open job", events)
	}
}
