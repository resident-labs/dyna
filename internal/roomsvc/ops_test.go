package roomsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workflowsvc"
	"dyna-agent/internal/workflowtemplate"
)

func TestRoomPreferencesAndEvents(t *testing.T) {
	e := newEnv(t)
	a := e.room("alpha")
	b := e.room("beta")
	offsets := map[string]int64{}
	sig := ""
	members := newMemberWatch()
	pins := map[string]string{}
	e.svc.scan(offsets, &sig, members, pins, true, func(Event) {})
	changes := 0
	scan := func() {
		t.Helper()
		before := changes
		e.svc.scan(offsets, &sig, members, pins, false, func(ev Event) {
			if ev.Kind == "rooms" {
				changes++
			}
		})
		if changes != before+1 {
			t.Fatal("missing room event")
		}
	}
	if err := e.svc.RenameRoom(a.ID, "New label"); err != nil {
		t.Fatal(err)
	}
	scan()
	rec, _ := e.svc.rooms().Resolve(a.ID)
	if rec.Name != "alpha" || rec.DisplayName != "New label" {
		t.Fatalf("rename changed routing: %+v", rec)
	}
	if err := e.svc.ArchiveRoom(a.ID); err != nil {
		t.Fatal(err)
	}
	scan()
	rec, _ = e.svc.rooms().Resolve(a.ID)
	if !rec.Archived(time.Now(), false) {
		t.Fatal("archive failed")
	}
	if err := e.svc.UnarchiveRoom(a.ID); err != nil {
		t.Fatal(err)
	}
	scan()
	rec, _ = e.svc.rooms().Resolve(a.ID)
	if rec.Archived(time.Now(), false) {
		t.Fatal("unarchive failed")
	}
	if err := e.svc.PinRoom(a.ID, 5); err != nil {
		t.Fatal(err)
	}
	scan()
	if err := e.svc.PinRoom(b.ID, 1); err != nil {
		t.Fatal(err)
	}
	scan()
	rows, err := e.svc.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ID != b.ID || rows[1].ID != a.ID {
		t.Fatalf("pin order: %+v", rows)
	}
	if err := e.svc.UnpinRoom(a.ID); err != nil {
		t.Fatal(err)
	}
	scan()
	if err := e.svc.PinRoom(a.ID, -1); err == nil {
		t.Fatal("negative order accepted")
	}
	if err := e.svc.RenameRoom(a.ID, "\n"); err == nil {
		t.Fatal("empty label accepted")
	}
	if err := e.svc.ArchiveRoom(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.rooms().Post(a.ID, room.PostInput{From: "alpha", To: []string{"alpha"}, Text: "revive"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = e.svc.rooms().Resolve(a.ID)
	if rec.ManualArchive {
		t.Fatal("post did not revive room")
	}
}

func TestWorkflowControlsUseService(t *testing.T) {
	e := newEnv(t)
	e.ctx = authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner})
	dir := t.TempDir()
	rec, err := e.svc.rooms().Create(room.CreateInput{Name: "workflow", Repo: dir})
	if err != nil {
		t.Fatal(err)
	}
	// This executable exits immediately. It starts no workflow workers.
	e.mgr.Executable = "/bin/true"
	t.Setenv("DYNA_SESSION", "parent-stamp")
	cat := workflowtemplate.New("")
	if _, err = cat.Save("demo", []byte("return args"), false); err != nil {
		t.Fatal(err)
	}
	templates, err := e.svc.ListTemplates()
	if err != nil || len(templates) != 1 {
		t.Fatalf("templates: %+v %v", templates, err)
	}
	input := map[string]any{"task": "a; $(do-not-execute)"}
	res, err := e.svc.StartRun(e.ctx, rec.ID, RunSource{Template: "demo"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := runstore.ValidateRunID(res.ID); err != nil {
		t.Fatal(err)
	}
	readOptions := func(id string) workflowsvc.Options {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(runstore.RunDir(id), "launch.json"))
		if err != nil {
			t.Fatal(err)
		}
		var opts workflowsvc.Options
		if err := json.Unmarshal(b, &opts); err != nil {
			t.Fatal(err)
		}
		return opts
	}
	opts := readOptions(res.ID)
	if opts.ScriptPath != templates[0].Path || opts.WorkDir != dir || opts.Args.(map[string]any)["task"] != input["task"] {
		t.Fatalf("options: %+v", opts)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.js"), []byte("return 1"), 0600); err != nil {
		t.Fatal(err)
	}
	custom, err := e.svc.StartRun(e.ctx, rec.ID, RunSource{Script: "custom.js"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if readOptions(custom.ID).ScriptPath != filepath.Join(dir, "custom.js") {
		t.Fatal("relative script not in repo")
	}
	if _, err = e.svc.StartRun(e.ctx, rec.ID, RunSource{}, nil); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err = e.svc.StartRun(e.ctx, rec.ID, RunSource{Template: "demo", Script: "x"}, nil); err == nil {
		t.Fatal("two sources accepted")
	}
	meta := runstore.Meta{ID: res.ID, Status: "running", Pid: -1}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(runstore.RunDir(res.ID), "meta.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CancelRun(e.ctx, res.ID); err != nil {
		t.Fatal(err)
	}
	got, err := runstore.ReadMeta(res.ID)
	if err != nil || got.Status != "canceled" {
		t.Fatalf("canceled run: %+v %v", got, err)
	}
	// The bare session stamp above tests child isolation. Clear it before
	// the separate room-deletion action.
	storetest.ClearStamp(t)
	if err := e.svc.DeleteRoom(e.ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.rooms().Resolve(rec.ID); err == nil {
		t.Fatal("deleted room still resolves")
	}
}

func TestSessionQueueServiceAndEvents(t *testing.T) {
	e := newEnv(t)
	rec := e.room("queue")
	info := e.start(rec, "worker", nil)
	msg, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: rec.Name, To: []string{info.Address()}, Text: "waiting input", Human: true})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := e.svc.SessionQueue(info.ID)
	if err != nil || len(queue) != 1 || !queue[0].Cancellable {
		t.Fatalf("queue: %+v %v", queue, err)
	}
	offsets := map[string]int64{}
	sig := ""
	members := newMemberWatch()
	pins := map[string]string{}
	e.svc.scan(offsets, &sig, members, pins, true, func(Event) {})
	if err = e.svc.DeletePendingSteer(info.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	changed := false
	e.svc.scan(offsets, &sig, members, pins, false, func(ev Event) {
		if ev.Kind == "members" && ev.RoomID == rec.ID {
			changed = true
		}
	})
	if !changed {
		t.Fatal("missing queue update event")
	}
	queue, err = e.svc.SessionQueue(info.ID)
	if err != nil || queue[0].State != "cancelled" {
		t.Fatalf("cancelled: %+v %v", queue, err)
	}
}

func TestDeleteRoomRefusesOpenSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("blocked-delete")
	info := e.start(rec, "idle", nil)
	if err := e.svc.DeleteRoom(e.ctx, rec.ID); err == nil || !strings.Contains(err.Error(), info.Address()) {
		t.Fatalf("delete with open session = %v", err)
	}
	if _, err := e.svc.rooms().Resolve(rec.ID); err != nil {
		t.Fatalf("blocked delete removed room: %v", err)
	}
}

func TestDeleteRoomSkipsUnassignedLegacyHistory(t *testing.T) {
	e := newEnv(t)
	rec := e.room("empty-with-legacy-history")
	id := subagent.NewID()
	raw := []byte(`{"id":"` + id + `","name":"cu_mobile","closed":"2026-10-05T00:00:00Z"}`)
	recordtest.WriteRaw(t, e.mgr.Store.Root, id, raw)
	if err := e.svc.DeleteRoom(e.ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	after, err := recordtest.ReadRaw(t, e.mgr.Store.Root, id)
	if err != nil || string(after) != string(raw) {
		t.Fatalf("legacy history changed: %s, %v", after, err)
	}
}

func TestRoomWatchRereadsClosedSessionsOnlyOnFullScans(t *testing.T) {
	e := newEnv(t)
	rec := e.room("closed-watch")
	info := e.start(rec, "done", nil)
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
		t.Fatal(err)
	}
	offsets, sig, members, pins := map[string]int64{}, "", newMemberWatch(), map[string]string{}
	e.svc.scan(offsets, &sig, members, pins, true, func(Event) {})
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= MemberFullScanEvery; i++ {
		changed := false
		e.svc.scan(offsets, &sig, members, pins, false, func(ev Event) {
			if ev.Kind == "members" && ev.Change != nil && ev.Change.ID == info.ID {
				changed = true
			}
		})
		if full := i == MemberFullScanEvery; changed != full {
			t.Fatalf("scan %d: closed session event = %v, want %v", i, changed, full)
		}
	}
}

// The run page opens an agent by its session ID. A run-owned session stays out
// of the member list, yet its transcript still loads.
func TestARunAgentSessionLoadsAlthoughMembersOmitIt(t *testing.T) {
	e := newEnv(t)
	rec := e.room("workers")
	t.Setenv("DYNA_RUN_ID", "wf_ops-open")
	t.Setenv("DYNA_ROOM_ID", rec.ID)
	t.Setenv("DYNA_ROOM", rec.Name)
	run, err := runstore.Create("open", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Finish("ok", "null", nil)
	agent, err := e.mgr.StartRunAgent(e.ctx, subagent.StartOptions{
		Run: &subagent.RunOwner{ID: run.Meta.ID, Agent: 1}, Profile: "mock", Dir: t.TempDir(), Room: rec.ID, Task: "SLEEP:0\nRESPOND: ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(agent.ID, 1)
	members, err := e.svc.Members(rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.ID == agent.ID {
			t.Fatalf("the member list holds run agent %s", agent.ID)
		}
	}
	got, err := e.svc.Transcript(agent.ID)
	if err != nil || got.Member.ID != agent.ID {
		t.Fatalf("Transcript(%s) = %+v, %v; want the session", agent.ID, got.Member, err)
	}
}
