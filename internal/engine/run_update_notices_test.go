package engine

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/replayreason"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestUpdateInterruptedRunReportsOnce(t *testing.T) {
	dir := storetest.DataDir(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	n := notify.New(&room.Store{Root: filepath.Join(root, "rooms")}, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}})
	rec, err := n.Rooms.Create(room.CreateInput{Name: "update-room"})
	if err != nil {
		t.Fatal(err)
	}
	meta := runstore.Meta{ID: "wf_update", Name: "workflow", Status: "running", ParentSession: "ses_closed", RoomID: rec.ID, Room: rec.Name, StartedAt: time.Now()}
	if _, err := db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,?,?)", meta.ID, "", meta.StartedAt.UnixNano(), string(arg(t, meta))); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the author post was saved, then the author closes.
	address := rec.Name + ":closed"
	_, err = n.Rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Producer: room.ProducerWorkflow,
		Event: room.RunEvent{RunID: meta.ID, Name: meta.Name, State: room.RunInterrupted},
		To:    []string{address}, SessionIDs: map[string]string{address: meta.ParentSession}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := websvc.Bind(runLifecycleBindings(n))
	if err != nil {
		t.Fatal(err)
	}
	(&socketCallers{runRoom: func(string) (string, error) { return rec.ID, nil }}).actForCallers(calls)
	applyOnce(calls, state.For(db), t.Logf)
	var caller atomic.Pointer[websvc.Caller]
	caller.Store(&websvc.Caller{Kind: websvc.Worker, Run: meta.ID})
	enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) { return *caller.Load(), nil }, calls)
	client := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	input := callspec.RunFinish{Run: meta.ID, Status: runstore.StatusInterrupted, Error: "workflow stopped for a dyna update: newer schema", EndedAt: time.Now(), DirectoriesComplete: true}
	for _, id := range []string{meta.ID + "/finish/1", meta.ID + "/finish/2"} {
		var finished runstore.Meta
		if err := client.Call(callspec.WithRequest(context.Background(), id), "FinishRun", []any{input}, &finished); err != nil {
			t.Fatal(err)
		}
	}
	messages, _, err := n.Rooms.Log(rec, room.LogOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	count, total := 0, 0
	for _, msg := range messages {
		ev, ok := msg.RunResult()
		if !ok {
			continue
		}
		total++
		if !msg.OwnerReport {
			continue
		}
		count++
		decision, _ := room.RootContext(msg, rec.Name)
		if ev.RunID != meta.ID || ev.State != room.RunInterrupted || ev.ReplayReason != replayreason.Legacy || !msg.Wake || decision != room.RootWake || !strings.Contains(ev.Detail, "--resume "+meta.ID) || !strings.Contains(ev.Detail, "repeat") {
			t.Fatalf("event = %+v, wake = %v; want a waking interrupted report and safe restart guidance", ev, msg.Wake)
		}
	}
	if count != 1 || total != 2 {
		t.Fatalf("root reports = %d, total = %d; want one original and one fallback", count, total)
	}
}
