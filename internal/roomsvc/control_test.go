package roomsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

func TestStopAndCloseAreRoomScoped(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	beta := e.room("beta")
	lead := e.start(alpha, "lead", nil)
	other := e.start(beta, "other", nil)

	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()
	if _, err := e.svc.ForceCloseSession(ctx, "alpha", other.ID); err == nil {
		t.Fatal("CloseSession closed a session in another room")
	}
	if _, err := e.svc.StopSession(ctx, "alpha", lead.ID); err == nil {
		t.Fatal("StopSession on an idle session must fail")
	}
	m, err := e.svc.ForceCloseSession(ctx, "alpha", lead.ID)
	if err != nil || !m.Closed || m.State != StateClosed {
		t.Fatalf("CloseSession = %+v, %v", m, err)
	}
	if info, _ := e.svc.sessions().Info(other.ID); info.Status == "closed" {
		t.Fatal("the other room's session was closed")
	}
}

func writeRun(t *testing.T, m runstore.Meta, events []runstore.Event, result string) {
	t.Helper()
	dir := filepath.Join(runstore.RunsDir(), m.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	runstoretest.Write(t, store.DataDir(), m)
	var lines []byte
	for _, ev := range events {
		l, _ := json.Marshal(ev)
		lines = append(append(lines, l...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	if result != "" {
		_ = os.WriteFile(filepath.Join(dir, "result.json"), []byte(result), 0o644)
	}
}

func TestRunsByRoomAndDetail(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	now := time.Now()
	writeRun(t, runstore.Meta{ID: "wf_20261016-000001-aaaaaaaa", Name: "review", Status: "ok", RoomID: alpha.ID, StartedAt: now.Add(-3 * time.Hour), EndedAt: now}, []runstore.Event{
		{T: "phase", Title: "Review"},
		{T: "agent_start", ID: 1, Label: "bugs", Profile: "mock", Phase: "Review", TS: 1},
		{T: "agent_end", ID: 1, Status: "ok", DurMs: 1200, Preview: "found 2"},
		{T: "agent_end", ID: 2, Label: "perf", Status: "error", Error: "boom"}, // no start event read
		{T: "agent_start", ID: 3, Label: "late"},                               // no end before the run ended
		{T: "log", Msg: "hello"},
		{T: "log", Msg: "internal", Provenance: "system"},
	}, `{"ok":true}`)
	// An older meta without RoomID lands in its parent session's room.
	writeRun(t, runstore.Meta{ID: "wf_20261016-000002-bbbbbbbb", Name: "legacy", Status: "running", ParentSession: lead.ID, StartedAt: now.Add(-2 * time.Hour)}, nil, "")
	// An untagged run with no parent has no room.
	writeRun(t, runstore.Meta{ID: "wf_20261016-000003-cccccccc", Name: "stray", Status: "ok", StartedAt: now.Add(-time.Hour)}, nil, "")

	list, err := e.svc.Runs("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || list.Running != 1 || len(list.Runs) != 2 || list.Runs[0].Name != "legacy" || list.Runs[0].Parent != "lead" || list.Runs[0].ParentID != lead.ID || list.Runs[1].Name != "review" {
		t.Fatalf("alpha runs = %+v", list)
	}

	if r := list.Runs[1]; r.Agents != 3 || r.Done != 2 || r.Failed != 1 || r.Working != 0 {
		t.Fatalf("review counts = %+v", r)
	}
	d, err := e.svc.RunDetail("wf_20261016-000001-aaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if d.AgentTotal != 3 || len(d.Agents) != 3 || len(d.Logs) != 1 || d.ResultBytes != 11 || d.ResultSummary != "true" || len(d.Phases) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	a := d.Agents
	if a[0].Status != "ok" || a[0].DurMs != 1200 || a[0].Preview != "found 2" || a[0].Started == 0 {
		t.Fatalf("agent 1 = %+v", a[0])
	}
	if a[1].Status != "error" || a[1].Label != "perf" || a[1].Started != 0 {
		t.Fatalf("agent 2 = %+v", a[1])
	}
	if a[2].Status != "unknown" {
		t.Fatalf("agent 3 = %+v; an agent with no end in a finished run is unknown", a[2])
	}
	if _, err := e.svc.RunDetail("../etc"); err == nil {
		t.Fatal("RunDetail accepted a bad id")
	}
}

func TestRunsShowWhenARunningRunIsPaused(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	now := time.Now()
	running := runstore.Meta{ID: "wf_20261016-000004-dddddddd", Name: "build", Status: "running", RoomID: alpha.ID, StartedAt: now}
	done := runstore.Meta{ID: "wf_20261016-000005-eeeeeeee", Name: "old", Status: "ok", RoomID: alpha.ID, StartedAt: now.Add(-time.Hour), EndedAt: now}
	writeRun(t, running, nil, "")
	writeRun(t, done, nil, "")
	for _, id := range []string{running.ID, done.ID} {
		if err := runstore.SetPaused(id, true); err != nil {
			t.Fatal(err)
		}
	}
	list, err := e.svc.Runs("alpha")
	if err != nil || len(list.Runs) != 2 || !list.Runs[0].Paused || list.Runs[1].Paused {
		t.Fatalf("runs = %+v %v; want only the running run paused", list, err)
	}
	if err := runstore.SetPaused(running.ID, false); err != nil {
		t.Fatal(err)
	}
	if d, err := e.svc.RunDetail(running.ID); err != nil || d.Run.Paused {
		t.Fatalf("detail after resume = %+v %v", d.Run, err)
	}
}

// Stale pause files do not override canonical state.
func TestRunsIgnoreStaleFileOnlyPause(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	running := runstore.Meta{ID: "wf_20261016-000006-ffffffff", Name: "old runner", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}
	writeRun(t, running, nil, "")
	// Cache the row before the earlier-release command creates its pause file.
	if list, err := e.svc.Runs("alpha"); err != nil || len(list.Runs) != 1 || list.Runs[0].Paused {
		t.Fatalf("runs before pause = %+v %v", list, err)
	}
	if err := os.WriteFile(filepath.Join(runstore.RunsDir(), running.ID, "paused"), []byte("paused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runstore.IsPaused(running.ID) {
		t.Fatal("IsPaused used a stale pause file")
	}
	if list, err := e.svc.Runs("alpha"); err != nil || len(list.Runs) != 1 || list.Runs[0].Paused {
		t.Fatalf("runs after file-only pause = %+v %v", list, err)
	}
	if d, err := e.svc.RunDetail(running.ID); err != nil || d.Run.Paused {
		t.Fatalf("detail after file-only pause = %+v %v", d.Run, err)
	}
	if err := runstore.SetPaused(running.ID, false); err != nil {
		t.Fatal(err)
	}
	if d, err := e.svc.RunDetail(running.ID); err != nil || d.Run.Paused {
		t.Fatalf("detail after resume = %+v %v", d.Run, err)
	}
}

func TestWatchEmitsRunsEvents(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	sigs := map[string]string{}
	var got []Event
	emit := func(ev Event) { got = append(got, ev) }
	e.svc.scanRuns(sigs, true, emit)
	writeRun(t, runstore.Meta{ID: "wf_20261016-000004-dddddddd", Name: "w", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}, nil, "")
	e.svc.scanRuns(sigs, false, emit)
	if len(got) != 1 || got[0].Kind != "runs" || got[0].RoomID != alpha.ID {
		t.Fatalf("events = %+v", got)
	}
	e.svc.scanRuns(sigs, false, emit)
	if len(got) != 1 {
		t.Fatalf("an unchanged store emitted %+v", got[1:])
	}
}

// TestRunCountsRecountWhenARunEnds: a crashed run's unfinished agent stops
// counting as working once the run is no longer running, although its
// events file did not grow.
func TestRunCountsRecountWhenARunEnds(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	m := runstore.Meta{ID: "wf_20261016-000011-abababab", Name: "crash", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}
	writeRun(t, m, []runstore.Event{{T: "agent_start", ID: 1, Label: "a"}}, "")
	if l, _ := e.svc.Runs("alpha"); l.Runs[0].Working != 1 {
		t.Fatalf("live run = %+v", l.Runs[0])
	}
	m.Status = "error"
	if err := runstore.ForceStatus(m.ID, m.Status, m.Error); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.svc.Runs("alpha"); l.Runs[0].Working != 0 {
		t.Fatalf("ended run still counts working agents: %+v", l.Runs[0])
	}
}

func TestPromotionIsRoomScopedAndCloseHandsChildrenUp(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("alpha"), e.room("beta")
	lead := e.start(alpha, "lead", nil)
	child := e.start(alpha, "child", &lead)
	grandchild := e.start(alpha, "grandchild", &child)
	other := e.start(beta, "other", nil)
	if _, err := e.svc.PromoteSession(context.Background(), "alpha", other.ID); err == nil {
		t.Fatal("promoted across rooms")
	}
	if _, err := e.svc.ForceCloseSession(e.ctx, "beta", lead.ID); err == nil {
		t.Fatal("closed across rooms")
	}
	if m, err := e.svc.PromoteSession(context.Background(), "alpha", grandchild.ID); err != nil || m.Parent != "" {
		t.Fatalf("promote = %+v, %v", m, err)
	}
	if m, err := e.svc.ForceCloseSession(e.ctx, "alpha", lead.ID); err != nil || !m.Closed {
		t.Fatalf("close = %+v, %v", m, err)
	}
	current, _ := e.mgr.Store.ReadMeta(child.ID)
	if current.ParentSession != "" || current.Depth != 1 || !current.Closed.IsZero() {
		t.Fatalf("child = %+v", current)
	}
	members, err := e.svc.Members("alpha", false)
	if err != nil || len(members) != 3 || members[0].Children != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	if _, err := e.svc.PromoteSession(context.Background(), "alpha", lead.ID); err == nil {
		t.Fatal("promoted closed session")
	}
}

func TestWatchEmitsMemberEventOnPromotion(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &lead)
	offsets, memberSig, pinSig := map[string]int64{}, newMemberWatch(), map[string]string{}
	roomSig := ""
	var got []Event
	emit := func(ev Event) { got = append(got, ev) }
	e.svc.scan(offsets, &roomSig, memberSig, pinSig, true, emit)
	if _, err := e.svc.PromoteSession(context.Background(), "alpha", child.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.scan(offsets, &roomSig, memberSig, pinSig, false, emit)
	for _, ev := range got {
		if ev.Kind == "members" && ev.RoomID == rec.ID {
			return
		}
	}
	t.Fatalf("no member event after promotion: %+v", got)
}

func TestFinishedSessionStaysMessageableAndReopenIsRoomScoped(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	beta := e.room("beta")
	lead := e.start(alpha, "lead", nil)
	other := e.start(beta, "other", nil)
	if _, err := e.svc.FinishGoal(t.Context(), alpha.ID, lead.Goal, "done", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Finish(e.ctx, lead.ID); err != nil {
		t.Fatal(err)
	}
	members, err := e.svc.Members(alpha.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var finished Member
	for _, m := range members {
		if m.ID == lead.ID {
			finished = m
		}
	}
	if !finished.Finished || finished.Closed || finished.Status != subagent.StatusDormant {
		t.Fatalf("finished member is not dormant: %+v", members)
	}
	if finished.Ended.IsZero() {
		t.Fatalf("a dormant member must carry the time it finished: %+v", finished)
	}
	if data, err := json.Marshal(finished); err != nil || !strings.Contains(string(data), `"ended":"`) {
		t.Fatalf("the members API row lacks ended: %s %v", data, err)
	}
	choices, err := e.svc.MentionChoices(alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(choices, "@lead") {
		t.Fatalf("a message must reach a dormant session: %v", choices)
	}
	if _, err := e.mgr.CloseWithOptions(e.ctx, other.ID, subagent.CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReopenSession(context.Background(), alpha.ID, other.ID); err == nil {
		t.Fatal("reopened another room's session")
	}
}

// A run result and each worker result load in full on demand, even past the
// old 64 KB cut, and RunDetail carries only their summary and size.
func TestRunResultLoadsInFull(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	var b strings.Builder
	b.WriteString(`{"results":[`)
	for i := 0; i < 14; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"tool":"tool-%d","summary":"%s"}`, i, strings.Repeat("x", 9800))
	}
	b.WriteString("]}")
	big := b.String()
	if len(big) < 133*1024 {
		t.Fatalf("fixture is %d bytes, want at least 133 KB", len(big))
	}
	id := "wf_20261016-000002-bbbbbbbb"
	writeRun(t, runstore.Meta{ID: id, Name: "big", Status: "ok", RoomID: alpha.ID, StartedAt: time.Now(), EndedAt: time.Now()}, []runstore.Event{
		{T: "agent_start", ID: 1, Label: "one"}, {T: "agent_end", ID: 1, Status: "ok"},
	}, big)
	journal := `{"id":1,"label":"one","profile":"mock","key":"k","prompt":"p","result":{"tool":"Tern","summary":"A Mac app."}}` + "\n"
	if err := os.WriteFile(filepath.Join(runstore.RunsDir(), id, "journal.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.RunDetail(id)
	if err != nil || d.ResultBytes != len(big) || !strings.HasPrefix(d.ResultSummary, "14 results: ") {
		t.Fatalf("RunDetail = bytes %d summary %q, %v", d.ResultBytes, d.ResultSummary, err)
	}
	full, err := e.svc.RunResult(id, 0)
	if err != nil || full.Result != big || full.TruncatedKB != 0 {
		t.Fatalf("RunResult(run) = %d bytes, truncated %d, %v", len(full.Result), full.TruncatedKB, err)
	}
	worker, err := e.svc.RunResult(id, 1)
	if err != nil || worker.Result != `{"tool":"Tern","summary":"A Mac app."}` {
		t.Fatalf("RunResult(worker) = %q, %v", worker.Result, err)
	}
	if _, err := e.svc.RunResult(id, 9); err == nil {
		t.Fatal("a missing worker result did not fail")
	}
}

// A result above the configured content limit loads truncated, with its size.
func TestRunResultTruncatesAtTheContentLimit(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	v, err := settings.Load(settings.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(settings.KeyMaxFileBytes, "1048576"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	id := "wf_20261016-000004-dddddddd"
	big := `{"text":"` + strings.Repeat("y", 2<<20) + `"}`
	writeRun(t, runstore.Meta{ID: id, Name: "huge", Status: "ok", RoomID: alpha.ID, StartedAt: time.Now(), EndedAt: time.Now()}, nil, big)
	got, err := e.svc.RunResult(id, 0)
	if err != nil || got.TruncatedKB != 1024 || len(got.Result) != 1<<20 || got.Bytes != len(big) {
		t.Fatalf("RunResult = %d bytes, truncated %d KB, size %d, %v", len(got.Result), got.TruncatedKB, got.Bytes, err)
	}
}

// A running worker shows what it does now; a finished one does not.
func TestRunDetailShowsRunningActivity(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	id := "wf_20261016-000005-eeeeeeee"
	writeRun(t, runstore.Meta{ID: id, Name: "live", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}, []runstore.Event{
		{T: "agent_start", ID: 1, Label: "busy"},
		{T: "agent_start", ID: 2, Label: "done"}, {T: "agent_end", ID: 2, Status: "ok"},
	}, "")
	for _, agent := range []int{1, 2} {
		path, err := runstore.AgentNormalizedOutputPath(id, agent)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"v":1,"kind":"tool","name":"webfetch","input":"https://x.dev"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := e.svc.RunDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range d.Agents {
		want := map[int]string{1: "webfetch: https://x.dev", 2: ""}[a.ID]
		if a.Doing.Text != want {
			t.Errorf("agent %d doing = %q, want %q", a.ID, a.Doing.Text, want)
		}
	}
}

// A worker's detail carries its journal, newest first, and a running worker
// with a journal but no result yet still loads.
func TestRunResultCarriesTheWorkerJournal(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	id := "wf_20261016-000006-ffffffff"
	writeRun(t, runstore.Meta{ID: id, Name: "journal", Status: "running", RoomID: alpha.ID, StartedAt: time.Now()}, []runstore.Event{{T: "agent_start", ID: 1, Label: "busy"}}, "")
	path, err := runstore.AgentJournalPath(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i, msg := range []string{"orient", "search"} {
		if err := runstore.AppendAgentJournalPath(path, runstore.AgentJournalEntry{TS: int64(1000 + i), Kind: "progress", Message: msg, Next: "next " + msg, Source: "agent"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.svc.RunResult(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != "" || len(got.Journal) != 2 || got.Journal[0].Message != "search" || got.Journal[0].Next != "next search" || got.Journal[1].TS != 1000 {
		t.Fatalf("RunResult = result %q, journal %+v", got.Result, got.Journal)
	}
}

// A live member says what it is doing now: its running tool with the
// readable summary and when the tool started. An idle member says nothing.
func TestLiveMemberShowsItsRunningTool(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	if _, err := e.svc.sessions().UpdateMeta(lead.ID, func(m *subagent.Meta) { m.Status = subagent.StatusRunning }); err != nil {
		t.Fatal(err)
	}
	path, err := e.svc.sessions().NormalizedPath(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"v":1,"kind":"tool","name":"Bash","state":"started","id":"t1","ts":1000,"input":"{\"description\":\"Write a 10 GB test file\"}"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	members, err := e.svc.Members(alpha.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.ID == lead.ID && (m.Doing.Text != "Bash: Write a 10 GB test file" || m.Doing.Since != 1000) {
			t.Fatalf("live member doing = %+v", m.Doing)
		}
	}
}

// A session or a steward that calls a session control through the engine
// may change only itself and its descendants, as the CLI's Target checked
// in its own process before. The root seat and the owner, which the web app
// and paired devices act as, still may change any session in the room.
func TestSessionControlsLetASessionActOnlyOnItsDescendants(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	child := e.start(alpha, "child", &lead)
	stranger := e.start(alpha, "stranger", nil)
	as := func(info subagent.Info) context.Context {
		return authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: info.ID, Room: alpha.ID})
	}
	for name, act := range map[string]func(context.Context) error{
		"stop":    func(ctx context.Context) error { _, err := e.svc.StopSession(ctx, "alpha", child.ID); return err },
		"promote": func(ctx context.Context) error { _, err := e.svc.PromoteSession(ctx, "alpha", child.ID); return err },
		"close":   func(ctx context.Context) error { _, err := e.svc.ForceCloseSession(ctx, "alpha", child.ID); return err },
		"dismiss": func(ctx context.Context) error { _, err := e.svc.DismissSession(ctx, "alpha", child.ID); return err },
	} {
		if err := act(as(stranger)); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
			t.Errorf("%s by a stranger session = %v; want the descendant refusal", name, err)
		}
	}
	steward := authority.WithActor(e.ctx, authority.Actor{Role: authority.Steward, Session: stranger.ID, Room: alpha.ID})
	if _, err := e.svc.StopSession(steward, "alpha", child.ID); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
		t.Errorf("a steward stopped a session outside its tree: %v", err)
	}
	if _, err := e.svc.PromoteSession(as(lead), "alpha", child.ID); err != nil {
		t.Fatalf("the parent could not promote its child: %v", err)
	}
	// The root seat runs in root mode, which the CLI's Target never limited:
	// it closes a top-level session outside its own tree.
	rootSeat := authority.WithActor(e.ctx, authority.Actor{Role: authority.Root, Session: lead.ID, Room: alpha.ID})
	if m, err := e.svc.ForceCloseSession(rootSeat, "alpha", stranger.ID); err != nil || !m.Closed {
		t.Fatalf("the root seat could not close a session outside its tree: %+v, %v", m, err)
	}
	if _, err := e.svc.ReopenSession(as(lead), "alpha", stranger.ID); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
		t.Fatalf("a session reopened another lead's session: %v", err)
	}
	owner := authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner})
	if m, err := e.svc.ForceCloseSession(owner, "alpha", lead.ID); err != nil || !m.Closed {
		t.Fatalf("the owner could not close a session: %+v, %v", m, err)
	}
}

// In the engine, promote and adopt check the caller on ctx. They once checked
// an empty actor instead, so the engine refused every caller, the owner too.
func TestPromoteAndAdoptCheckTheCallerInTheEngine(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	child := e.start(alpha, "child", &lead)
	t.Cleanup(authority.InEngine())
	parent := authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: lead.ID, Room: alpha.ID})
	if m, err := e.svc.PromoteSession(parent, "alpha", child.ID); err != nil || m.Parent != "" {
		t.Fatalf("the parent could not promote its child in the engine: %+v, %v", m, err)
	}
	owner := authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner})
	if _, err := e.svc.Notify.Sessions.Adopt(owner, child.ID, lead.ID); err != nil {
		t.Fatalf("the owner could not adopt a session in the engine: %v", err)
	}
}

// RenameSession and AdoptSession act for the caller in the engine: a session
// changes only its own tree, and the owner changes any session.
func TestRenameAndAdoptSessionCheckTheCaller(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	child := e.start(alpha, "child", &lead)
	stranger := e.start(alpha, "stranger", nil)
	t.Cleanup(authority.InEngine())
	as := func(info subagent.Info) context.Context {
		return authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: info.ID, Room: alpha.ID})
	}
	if _, err := e.svc.RenameSession(as(stranger), "alpha", child.ID, "taken"); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
		t.Fatalf("a stranger renamed a session outside its tree: %v", err)
	}
	if _, err := e.svc.AdoptSession(as(stranger), "alpha", child.ID, stranger.ID); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
		t.Fatalf("a stranger adopted a session outside its tree: %v", err)
	}
	if m, err := e.svc.RenameSession(as(lead), "alpha", child.ID, "helper"); err != nil || m.Name != "helper" {
		t.Fatalf("the parent could not rename its child: %+v, %v", m, err)
	}
	owner := authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner})
	if m, err := e.svc.AdoptSession(owner, "alpha", child.ID, stranger.ID); err != nil || m.Parent != stranger.ID {
		t.Fatalf("the owner could not adopt a session: %+v, %v", m, err)
	}
}

// FinishSession acts for the caller in the engine: a session finishes only
// itself and its descendants. The Manager's own rule names no target, so
// this check is the one that keeps a session from finishing a stranger.
func TestFinishSessionChecksTheCaller(t *testing.T) {
	e := newEnv(t)
	alpha := e.room("alpha")
	lead := e.start(alpha, "lead", nil)
	child := e.start(alpha, "child", &lead)
	stranger := e.start(alpha, "stranger", nil)
	t.Cleanup(authority.InEngine())
	as := func(info subagent.Info) context.Context {
		return authority.WithActor(e.ctx, authority.Actor{Role: authority.Session, Session: info.ID, Room: alpha.ID})
	}
	if _, err := e.svc.FinishSession(as(stranger), "alpha", child.ID, ""); err == nil || !strings.Contains(err.Error(), "may act only on itself and its descendants") {
		t.Fatalf("a stranger finished a session outside its tree: %v", err)
	}
	// The child still holds its spawn goal, so the finish that passes the
	// caller check stops at the responsibility check.
	if _, err := e.svc.FinishSession(as(lead), "alpha", child.ID, ""); err == nil || !strings.Contains(err.Error(), "responsibilities: cannot finish") {
		t.Fatalf("the parent's finish of its child = %v; want it past the caller check", err)
	}
}

// StartSubagent acts for the caller in the engine. A workflow worker may
// not start a session. Session, root and steward callers start children
// only in their own room, and the owner
// starts top-level sessions in the caller's directory. The start event
// records that directory.
func TestStartSubagentActsForTheCaller(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("alpha"), e.room("beta")
	lead := e.start(alpha, "lead", nil)
	t.Cleanup(authority.InEngine())
	as := func(actor authority.Actor) context.Context { return authority.WithActor(e.ctx, actor) }
	in := SubagentStart{Profile: "mock", Goal: "Finish the test task", Task: "SLEEP:0\nRESPOND: ready", Cwd: t.TempDir()}
	worker := authority.Actor{Role: authority.Worker, Run: "wf_20261020-000001-aaaaaaaa", Agent: 1, Room: alpha.ID}
	if _, err := e.svc.StartSubagent(as(worker), "alpha", in); err == nil || !strings.Contains(err.Error(), "subagent start is") {
		t.Fatalf("workflow worker started a session: %v", err)
	}
	for _, role := range []authority.Role{authority.Session, authority.Root, authority.Steward} {
		actor := authority.Actor{Role: role, Session: lead.ID, Room: alpha.ID}
		if _, err := e.svc.StartSubagent(as(actor), beta.Name, in); err == nil || !strings.Contains(err.Error(), "another room") {
			t.Fatalf("%s started a child in another room: %v", role, err)
		}
		in.Name = "kid-" + string(role)
		kid, err := e.svc.StartSubagent(as(actor), "alpha", in)
		if err != nil || kid.ParentSession != lead.ID || kid.Depth != 2 || kid.Owner != lead.ID || kid.Dir != lead.Dir {
			t.Fatalf("%s child = parent %q depth %d owner %q dir %q, %v; want under %s in its directory", role, kid.ParentSession, kid.Depth, kid.Owner, kid.Dir, err, lead.ID)
		}
		e.wait(kid.ID, 1)
	}
	in.Name, in.Owner = "top", "codex:thread"
	top, err := e.svc.StartSubagent(as(authority.Actor{Role: authority.Owner}), beta.Name, in)
	if err != nil || top.ParentSession != "" || top.Owner != "codex:thread" || top.Dir != in.Cwd {
		t.Fatalf("owner's session = parent %q owner %q dir %q, %v; want top-level in %s", top.ParentSession, top.Owner, top.Dir, err, in.Cwd)
	}
	e.wait(top.ID, 1)
	dir, err := e.mgr.Store.Dir(top.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"cwd":"`+in.Cwd+`"`) {
		t.Fatalf("start event has no cwd %s: %s, %v", in.Cwd, raw, err)
	}
}
