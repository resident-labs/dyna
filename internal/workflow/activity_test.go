package workflow

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
)

func TestCommandStepActivityKeepsStderrUnknown(t *testing.T) {
	dir := t.TempDir()
	if err := activity.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE runs(id TEXT PRIMARY KEY); CREATE TABLE session_records(id TEXT PRIMARY KEY); INSERT INTO runs(id) VALUES('run1')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	e := &engine{opts: Options{Run: &runstore.Run{Dir: filepath.Join(dir, "runs", "run1"), Meta: runstore.Meta{ID: "run1"}}}, shared: &sharedState{}}
	key := activity.Key{Kind: activity.CommandStep, Owner: "run1", Number: 1}
	e.observeActivity(runstore.Event{T: "exec_start", ID: 1})
	pub := e.shared.activities[key].publisher
	e.observeActivity(runstore.Event{T: "exec_run", ID: 1, Shell: "sh_test"})
	e.observeActivity(runstore.Event{T: "exec_end", ID: 1})
	<-pub.Done()
	got, err := activity.Read(db, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != activity.CopierEnded || got.Signal.Copier != activity.CopierUnknown || got.Reason != activity.ReasonStderrUnobserved || got.Shell != "sh_test" {
		t.Fatalf("activity = %+v", got)
	}
}
