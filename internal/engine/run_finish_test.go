package engine

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestFinishRunPreservesFirstOutcomeAndReceipt(t *testing.T) {
	dir := storetest.DataDir(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const run = "wf_finish"
	workflowSpoolRun(t, db, run, "running")
	calls, err := websvc.Bind(RunLifecycleBindings())
	if err != nil {
		t.Fatal(err)
	}
	(&socketCallers{runRoom: func(string) (string, error) { return "", nil }}).actForCallers(calls)
	applyOnce(calls, state.For(db), t.Logf)
	var caller atomic.Pointer[websvc.Caller]
	caller.Store(&websvc.Caller{Kind: websvc.Worker, Run: run})
	enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) { return *caller.Load(), nil }, calls)
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	ended := time.Unix(123, 0).UTC()
	input := map[string]any{"run": run, "status": "ok", "endedAt": ended, "directoriesComplete": true}
	finish := func(id string) (runstore.Meta, error) {
		var meta runstore.Meta
		err := client.Call(callspec.WithRequest(context.Background(), id), "FinishRun", []any{input}, &meta)
		return meta, err
	}
	id := run + "/finish/1"
	if _, err := db.Exec("CREATE TRIGGER refuse_finish_receipt BEFORE INSERT ON request_ids BEGIN SELECT RAISE(ABORT, 'receipt refused'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := finish(id); err == nil {
		t.Fatal("finish succeeded without its receipt")
	}
	before, err := runstore.LoadStored(t.Context(), db, run)
	if err != nil || before.Status != "running" {
		t.Fatalf("finish escaped rollback: %+v, %v", before, err)
	}
	if _, err := db.Exec("DROP TRIGGER refuse_finish_receipt"); err != nil {
		t.Fatal(err)
	}
	first, err := finish(id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ok" || !first.EndedAt.Equal(ended) {
		t.Fatalf("first result: %+v", first)
	}
	set, found, err := runstore.ReadDirectories(t.Context(), run)
	if err != nil || !found || !set.Complete {
		t.Fatalf("directory completion: %+v, %v", set, err)
	}
	input["status"] = "error"
	input["error"] = "later"
	input["directoriesComplete"] = false
	second, err := finish(run + "/finish/2")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("terminal result changed: %s != %s", a, b)
	}
	input["status"] = "ok"
	delete(input, "error")
	input["directoriesComplete"] = true
	replay, err := finish(id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatalf("receipt changed: %s != %s", a, b)
	}
	other := *caller.Load()
	other.Agent = 1
	caller.Store(&other)
	if _, err := finish(id); err == nil {
		t.Fatal("wrong worker replayed coordinator receipt")
	}
}
