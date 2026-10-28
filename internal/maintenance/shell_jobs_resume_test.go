package maintenance

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store/storetest"
)

func TestShellJobPruningResumesNextPass(t *testing.T) {
	dataDir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dataDir) })
	shs := &shells.Store{Root: filepath.Join(dataDir, "shells")}
	now := time.Now()
	old := now.Add(-4 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if err := shs.Save(shells.Meta{ID: "sh_20261022-120000-00000001", Cmd: []string{"true"}, Cwd: "/", Started: now.Add(-4 * 24 * time.Hour)}, &shells.Exit{Ended: now.Add(-4 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 10000)
INSERT INTO shell_jobs(id,name,cmd,cwd,owner,keep,room_id,room,pid,supervisor,supervisor_start,started,ended,exit_code,exit_signal,exit_error)
SELECT printf('sh_resume_%05d',x),'','[]','/','',0,'','',0,0,0,?,?,0,'','' FROM c`, old, old)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pruneShellJobs(context.Background(), shs, now); err != nil || got != 10000 {
		t.Fatalf("first pass removed %d rows, %v; want a bounded 10,000", got, err)
	}
	if got, err := pruneShellJobs(context.Background(), shs, now); err != nil || got != 1 {
		t.Fatalf("second pass removed %d rows, %v; want the remaining row", got, err)
	}
}
