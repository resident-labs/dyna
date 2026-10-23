package engine

import (
	"context"
	"net"
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestDirectoryReceiptRollsBackWithWrite(t *testing.T) {
	dir := storetest.DataDir(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const run = "wf_dirs"
	workflowSpoolRun(t, db, run, "running")
	calls, err := websvc.Bind(RunLifecycleBindings())
	if err != nil {
		t.Fatal(err)
	}
	(&socketCallers{runRoom: func(string) (string, error) { return "", nil }}).actForCallers(calls)
	applyOnce(calls, state.For(db), t.Logf)
	caller := websvc.Caller{Kind: websvc.Worker, Run: run}
	enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) { return caller, nil }, calls)
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	add := func(id string) error {
		return client.Call(callspec.WithRequest(context.Background(), id), "AddRunDirectories", []any{callspec.RunDirectories{Run: run, Directories: []string{"/tmp/a"}}}, nil)
	}

	if _, err := db.Exec(`CREATE TRIGGER refuse_receipt BEFORE INSERT ON request_ids BEGIN SELECT RAISE(ABORT, 'receipt refused'); END`); err != nil {
		t.Fatal(err)
	}
	id := run + "/dirs/1"
	if err := add(id); err == nil {
		t.Fatal("write succeeded without its receipt")
	}
	if _, found, err := runstore.ReadDirectories(t.Context(), run); err != nil || found {
		t.Fatalf("evidence committed without its receipt: found=%v err=%v", found, err)
	}
	if _, err := db.Exec("DROP TRIGGER refuse_receipt"); err != nil {
		t.Fatal(err)
	}
	if err := add(id); err != nil {
		t.Fatal(err)
	}
	set, found, err := runstore.ReadDirectories(t.Context(), run)
	if err != nil || !found || len(set.Directories) != 1 || set.Complete {
		t.Fatalf("retry evidence: %+v found=%v err=%v", set, found, err)
	}
	if _, done, err := state.For(db).RequestDone(t.Context(), runnerRequestKey(id)); err != nil || !done {
		t.Fatalf("retry receipt: done=%v err=%v", done, err)
	}
}
