package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/instructions"
	"dyna-agent/internal/shells"
)

// runningJob records a job this test process supervises, so the store
// reports it running for as long as the test runs.
func runningJob(t *testing.T, s *shells.Store, owner, name string) shells.Meta {
	t.Helper()
	m, err := s.Create(shells.StartOptions{Name: name, Cmd: []string{"sleep", "60"}, Cwd: t.TempDir(), Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	m.Supervisor = os.Getpid()
	if err := s.Save(m, nil); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunningJobsListOnlyOwnJobsAndDriftAsANote(t *testing.T) {
	e := newTestEnv(t)
	mine, _ := e.start("SLEEP:0\nRESPOND: ready")
	other, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(mine.ID, 1)
	e.wait(other.ID, 1)
	me := mine.Meta
	if strings.Contains(e.m.sessionInstructions(me, "owner", 3, "").Text(), "[DYNA RUNNING JOBS]") {
		t.Fatal("a session with no jobs got a running jobs section")
	}
	s := &shells.Store{Root: filepath.Join(filepath.Dir(e.m.store().root()), "shells")}
	build := runningJob(t, s, me.ID, "Run the  web\ntests")
	runningJob(t, s, other.ID, "Someone else's job")
	frozen := e.m.sessionInstructions(me, "owner", 3, "")
	text := frozen.Text()
	for _, want := range []string{"[DYNA RUNNING JOBS]", "- " + build.ID + " · Run the web tests · started " + build.Started.UTC().Format("2006-01-02 15:04 UTC"), "dyna shell tail <id>", "dyna shell stop <id>"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Someone else's job") {
		t.Fatal("another session's job appeared")
	}

	// A finished job reaches the session as a drift note; the frozen prompt
	// is not rebuilt.
	if err := s.Save(build, &shells.Exit{Code: 0, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if instructions.Refresh(frozen, false, false, false) {
		t.Fatal("a finished job rebuilt the prompt")
	}
	note := instructions.Note(frozen, e.m.sessionInstructions(me, "owner", 3, ""))
	if !strings.Contains(note, "- Your running jobs: removed \"- "+build.ID+" ·") {
		t.Fatalf("note:\n%s", note)
	}
}

// Through the session's own note path, on a real session.
func TestFinishedJobReachesTheSessionAsANote(t *testing.T) {
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:0\nRESPOND: ready")
	e.wait(info.ID, 1)
	s := &shells.Store{Root: filepath.Join(filepath.Dir(e.m.store().root()), "shells")}
	job := runningJob(t, s, info.ID, "Watch the build")
	if err := e.m.noteInstructions(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	before, err := e.m.Store.ReadMeta(info.ID)
	if err != nil || len(before.InstructionsNotes) != 1 {
		t.Fatalf("a started job posted no note: %+v %v", before.InstructionsNotes, err)
	}
	if err := s.Save(job, &shells.Exit{Code: 0, Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.noteInstructions(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	after, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Instructions.Hash != before.Instructions.Hash || after.Turns != 1 || len(after.InstructionsNotes) != 2 {
		t.Fatalf("the finished job rebuilt the prompt or posted no note: notes %v, turns %d", after.InstructionsNotes, after.Turns)
	}
	note, err := e.m.rooms().Message(e.room.ID, after.InstructionsNotes[1])
	if err != nil || note.Kind != "session" || note.Notice.Kind != "instructions" || note.Wake || !strings.Contains(note.Text, "Your running jobs: removed \"- "+job.ID) {
		t.Fatalf("note: %+v %v", note, err)
	}
}
