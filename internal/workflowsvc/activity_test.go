package workflowsvc

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/activity"
	"dyna-agent/internal/database"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
)

func TestStepActivityLinksStdoutButKeepsStderrUnknown(t *testing.T) {
	dir := storetest.DataDir(t)
	if err := activity.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := shells.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(`CREATE TABLE runs(id TEXT PRIMARY KEY); CREATE TABLE session_records(id TEXT PRIMARY KEY); INSERT INTO runs(id) VALUES('r')`); err != nil {
		t.Fatal(err)
	}
	shell, err := shells.DefaultStore().Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	output := now.Add(-time.Second)
	if _, err := db.Exec(`UPDATE shell_jobs SET activity_output=?, activity_lease=?, activity_copier=? WHERE id=?`, output.UnixNano(), now.Add(activity.Lease).UnixNano(), activity.CopierRunning, shell.ID); err != nil {
		t.Fatal(err)
	}
	key := activity.Key{Kind: activity.CommandStep, Owner: "r", Number: 1}
	rec := activity.Record{Signal: activity.Signal{CPU: activity.CPUUnsupported, LeaseUntil: now.Add(activity.Lease)}, Lifecycle: activity.CopierRunning, Reason: activity.ReasonStderrUnobserved, Shell: shell.ID}
	if err := activity.Put(context.Background(), db, key, rec); err != nil {
		t.Fatal(err)
	}
	got, err := StepActivity("r", 1, activity.CommandStep, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != activity.CopierRunning || got.Signal.Copier != activity.CopierUnknown || got.Reason != activity.ReasonStderrUnobserved || !got.Signal.LastOutput.Equal(output) {
		t.Fatalf("step activity = %+v", got)
	}
}
