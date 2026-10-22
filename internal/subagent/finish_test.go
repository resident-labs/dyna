package subagent

import (
	"context"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

func TestFinishMidTurnMakesSessionDormantAndAMessageWakesIt(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("RESPOND: final SLEEP:500")
	info = e.waitStatus(info.ID, StatusRunning)
	stampParent(t, info)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	info, turn := e.wait(info.ID, 1)
	if turn.Status != TurnOK || !strings.Contains(turn.Result.(string), "final") {
		t.Fatalf("finish lost the final response: %+v", turn)
	}
	if info.Status != StatusDormant || info.Finished.IsZero() {
		t.Fatalf("finished session is not dormant: %+v", info)
	}
	if len(RoomMembership([]Info{info}, info.RoomID, false).Sessions) != 1 {
		t.Fatal("dormant session left the member list")
	}
	if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{From: info.Room, To: []string{info.Address()}, Text: "informational notice", Wake: false}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.TickSessionTargets(e.ctx, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	afterNotice, err := e.m.Store.Info(info.ID)
	if err != nil || afterNotice.Status != StatusDormant || afterNotice.Turns != 1 {
		t.Fatalf("notice woke dormant session: %+v %v", afterNotice, err)
	}
	if _, err := e.send(info.ID, "RESPOND: woken SLEEP:1", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	info, turn = e.wait(info.ID, 2)
	if turn.Status != TurnOK || info.Status != StatusDormant || info.Finished.IsZero() {
		t.Fatalf("unclaimed wake did not return to dormant: %+v %+v", info, turn)
	}
}

func TestFinishIsBlockedByEachResponsibilityKind(t *testing.T) {
	for _, kind := range []string{"goal", "question", "pr author", "pr reviewer", "child", "shell", "workflow"} {
		t.Run(kind, func(t *testing.T) {
			e := newTestEnv(t)
			e.joinRunners()
			info, _ := e.start("SLEEP:1 RESPOND: idle")
			info, _ = e.wait(info.ID, 1)
			e.doneGoals()
			want := ""
			base := filepath.Dir(e.m.Store.root())
			switch kind {
			case "goal":
				g, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "unfinished", Holders: []string{info.ID}})
				if err != nil {
					t.Fatal(err)
				}
				want = "holds goal " + g.ID
			case "question":
				q, err := e.m.questionStore().Ask(questions.Question{Room: info.RoomID, Asker: info.ID, To: "owner", Text: "Which option?", Meanwhile: "continue"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				want = "awaits question " + q.ID
			case "pr author":
				writePRLink(t, info, "author", "open", true)
				want = "dyna git pr transfer"
			case "pr reviewer":
				writePRLink(t, info, "assigned reviewer", "open", true)
				want = "reviews PR #17"
			case "child":
				// A child delegates from the parent's goal, so the parent holds one.
				if _, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "protecting", Holders: []string{info.ID}}); err != nil {
					t.Fatal(err)
				}
				stampParent(t, info)
				child, _ := e.start("SLEEP:1 RESPOND: child")
				e.wait(child.ID, 1)
				want = "unfinished child " + child.Address()
			case "shell":
				job := "sh_20261013-184235-2b655443"
				want = "live shell job " + job
				st := shells.Store{Root: filepath.Join(base, "shells")}
				if err := st.Save(shells.Meta{ID: job, Owner: info.ID, Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}, nil); err != nil {
					t.Fatal(err)
				}
			case "workflow":
				dir, job, text := "runs", "wf_20261013-184235-2b655443", `{"id":"JOB","parentSession":"OWNER","status":"running","pid":PID}`
				want = "live workflow run " + job
				path := filepath.Join(base, dir, job)
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				text = strings.NewReplacer("JOB", job, "OWNER", info.ID, "PID", strconv.Itoa(os.Getpid())).Replace(text)
				if err := os.WriteFile(filepath.Join(path, "meta.json"), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := e.m.Finish(e.ctx, info.ID)
			if err == nil || !strings.HasPrefix(err.Error(), "responsibilities: cannot finish") || !strings.Contains(err.Error(), want) {
				t.Fatalf("finish error = %v, want one naming %q", err, want)
			}
			if meta, _ := e.m.Store.ReadMeta(info.ID); !meta.Finished.IsZero() || !meta.Closed.IsZero() {
				t.Fatalf("refused finish changed the session: %+v", meta)
			}
		})
	}
}

// A finished child owns nothing, so it does not block its parent.
func TestFinishedChildDoesNotBlockParent(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	parent, _ := e.start("SLEEP:1 RESPOND: parent")
	parent, _ = e.wait(parent.ID, 1)
	stampParent(t, parent)
	child, _ := e.start("SLEEP:1 RESPOND: child")
	child, _ = e.wait(child.ID, 1)
	if _, err := e.m.goalStore().SetItemState(child.RoomID, child.Goal, child.Item, child.ID, goals.ItemDone); err != nil {
		t.Fatal(err)
	}
	if _, err := e.finish(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.finish(parent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCloseServiceSessionClosesItsChildren(t *testing.T) {
	e := newTestEnv(t)
	svc, _ := e.start("SLEEP:1 RESPOND: reviewer", func(o *StartOptions) { o.Service = true })
	svc, _ = e.wait(svc.ID, 1)
	stampParent(t, svc)
	child, _ := e.start("SLEEP:1 RESPOND: helper")
	e.wait(child.ID, 1)
	stampParent(t, child)
	gc, _ := e.start("SLEEP:1 RESPOND: grandchild")
	e.wait(gc.ID, 1)
	if _, err := e.m.CloseWithOptions(e.ctx, svc.ID, CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{svc.ID, child.ID, gc.ID} {
		if got, _ := e.m.Store.Info(id); got.Status != StatusClosed {
			t.Fatalf("%s status = %s, want closed", got.Name, got.Status)
		}
	}
}

// Close closes children that own nothing and moves the rest to its parent.
func TestCloseHandsChildrenToItsParent(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	stampParent(t, lead)
	mid, _ := e.start("SLEEP:1 RESPOND: mid")
	mid, _ = e.wait(mid.ID, 1)
	stampParent(t, mid)
	busy, _ := e.start("SLEEP:1 RESPOND: busy")
	e.wait(busy.ID, 1)
	idle, _ := e.start("SLEEP:1 RESPOND: idle")
	e.wait(idle.ID, 1)
	// busy keeps its item; idle and mid own nothing.
	for _, id := range []string{idle.ID, mid.ID} {
		info, _ := e.m.Store.Info(id)
		if _, err := e.m.goalStore().SetItemState(info.RoomID, info.Goal, info.Item, "owner", goals.ItemDone); err != nil {
			t.Fatal(err)
		}
	}
	stampParent(t, lead)
	if _, err := e.m.CloseWithOptions(e.ctx, mid.ID, CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.m.Store.Info(idle.ID); got.Status != StatusClosed {
		t.Fatalf("child that owns nothing stayed open: %+v", got)
	}
	got, _ := e.m.Store.Info(busy.ID)
	if got.Status == StatusClosed || got.ParentSession != lead.ID || got.Depth != lead.Depth+1 {
		t.Fatalf("child with an item was not handed to the lead: %+v", got)
	}
}

func TestCloseRefusesPRRoleUnlessForced(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: author")
	info, _ = e.wait(info.ID, 1)
	writePRLink(t, info, "author", "open", true)
	if _, err := e.m.Close(e.ctx, info.ID); err == nil || !strings.HasPrefix(err.Error(), "responsibilities: cannot close") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("close error = %v", err)
	}
	if closed, err := e.m.CloseWithOptions(e.ctx, info.ID, CloseOptions{Force: true}); err != nil || closed.Status != StatusClosed {
		t.Fatalf("forced close: %+v %v", closed, err)
	}
}

func TestLegacyDoneSessionReadsAsDormant(t *testing.T) {
	e := newTestEnv(t)
	closed := time.Now().Add(-time.Hour).Truncate(time.Second)
	meta := Meta{ID: NewID(), RoomID: e.room.ID, Room: e.room.Name, Closed: closed}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	b = append(b[:len(b)-1], []byte(`,"done":true}`)...)
	writeRoomScanFixture(t, e, meta.ID, b)
	got, err := e.m.Store.Info(meta.ID)
	if err != nil || got.Status != StatusDormant || !got.Finished.Equal(closed) {
		t.Fatalf("legacy done session = %+v %v, want dormant", got, err)
	}
}

// Only finish makes a session dormant. An idle session that owns work is never
// dormant, so the sweep never closes it, however long it waits.
func TestSweepClosesOnlyLongDormantSessions(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	start := func() Info {
		info, _ := e.start("SLEEP:1 RESPOND: idle")
		info, _ = e.wait(info.ID, 1)
		return info
	}
	old, recent, late, author, idle := start(), start(), start(), start(), start()
	e.doneGoals()
	for _, info := range []Info{old, recent, late} {
		if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
			t.Fatal(err)
		}
	}
	writePRLink(t, author, "author", "open", true)
	if _, err := e.m.Finish(e.ctx, author.ID); err == nil {
		t.Fatal("a session with a PR finished")
	}
	// late finished just now after a long idle turn, so it is not yet long dormant.
	for id, age := range map[string]time.Duration{old.ID: DormantCloseAfter + time.Hour, recent.ID: DormantCloseAfter - time.Hour, late.ID: 30 * DormantCloseAfter, author.ID: 30 * DormantCloseAfter, idle.ID: 30 * DormantCloseAfter} {
		if _, err := e.m.Store.UpdateMeta(id, func(m *Meta) {
			m.LastActivity = time.Now().Add(-age)
			if !m.Finished.IsZero() && id != late.ID {
				m.Finished = m.LastActivity
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.SweepDormantSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	for id, wantClosed := range map[string]bool{old.ID: true, recent.ID: false, late.ID: false, author.ID: false, idle.ID: false} {
		got, _ := e.m.Store.Info(id)
		if (got.Status == StatusClosed) != wantClosed {
			t.Fatalf("%s status = %s, want closed=%v", got.Name, got.Status, wantClosed)
		}
		if id == author.ID && !got.Finished.IsZero() {
			t.Fatal("a session with a PR became dormant")
		}
	}
}

// New work clears dormancy, so Clear dormant skips its owner.
func TestClearDormantClosesOnlySessionsThatOwnNothing(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	start := func() Info {
		info, _ := e.start("SLEEP:1 RESPOND: idle")
		info, _ = e.wait(info.ID, 1)
		return info
	}
	empty, busy, idle := start(), start(), start()
	e.doneGoals()
	for _, info := range []Info{empty, busy} {
		if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.m.goalStore().Add(busy.RoomID, "owner", goals.Input{Text: "new work", Holders: []string{busy.ID}}); err != nil {
		t.Fatal(err)
	}
	stampParent(t, idle)
	if _, err := e.m.ClearDormantSessions(e.ctx, empty.RoomID); err == nil {
		t.Fatal("an agent cleared dormant sessions")
	}
	ownerDismissEnv(t)
	result, err := e.m.ClearDormantSessions(e.ctx, empty.RoomID)
	if err != nil || result.Succeeded != 1 || result.Failed != 0 {
		t.Fatalf("clear = %+v, %v", result, err)
	}
	for id, wantClosed := range map[string]bool{empty.ID: true, busy.ID: false, idle.ID: false} {
		if got, _ := e.m.Store.Info(id); (got.Status == StatusClosed) != wantClosed {
			t.Fatalf("%s status = %s, want closed=%v", got.Name, got.Status, wantClosed)
		}
	}
}

// Finish holds its reservation from the check to the commit, so new work
// assigned inside that window is refused instead of lost.
func TestFinishRefusesWorkAssignedDuringTheCheck(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	e.doneGoals()
	var assignErr, startErr error
	e.m.checkpoint = func(stage string) {
		if stage != "finish-checked" {
			return
		}
		_, assignErr = e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "late work", Holders: []string{info.ID}})
		stampParent(t, info)
		_, _, startErr = e.m.Start(context.Background(), StartOptions{Goal: "late child", Profile: "mock", Dir: t.TempDir(), Task: "RESPOND: child"})
	}
	if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if assignErr == nil || startErr == nil {
		t.Fatalf("work arrived during the finish check: goal %v, child %v", assignErr, startErr)
	}
	got, _ := e.m.Store.Info(info.ID)
	items, err := e.m.Responsibilities(e.ctx, got)
	if err != nil || len(items) != 0 || got.Finished.IsZero() || got.Closing != nil {
		t.Fatalf("finished session = %+v, responsibilities %+v, %v", got.Meta, items, err)
	}
}

// Dismiss commits only while the session is still finished and idle. A message
// that arrives after the reservation keeps the session open.
func TestDismissKeepsSessionWokenAfterTheCheck(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	e.m.checkpoint = func(stage string) {
		if stage == "dormant-reserved" {
			_, _ = e.send(info.ID, "RESPOND: woken SLEEP:1", WakeOptions{})
		}
	}
	ownerDismissEnv(t)
	if _, err := e.m.DismissSession(e.ctx, info.ID); err == nil {
		t.Fatal("dismissal closed a session that a message woke")
	}
	e.m.checkpoint = nil
	if got, _ := e.m.Store.Info(info.ID); got.Status == StatusClosed {
		t.Fatalf("woken session closed: %+v", got.Meta)
	}
}

// Only the owner dismisses dormant sessions. The internal root is the owner;
// a stamped subagent is not.
func TestDismissSessionAllowsRootModeOnly(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, "ses_20260101-120000-abcdef12")
	if _, err := e.m.DismissSession(e.ctx, info.ID); err == nil {
		t.Fatal("a subagent dismissed a session")
	}
	if _, err := e.m.ClearDormantSessions(e.ctx, info.RoomID); err == nil {
		t.Fatal("a subagent cleared dormant sessions")
	}
	rootModeEnv(t, info.RoomID)
	if _, err := e.m.DismissSession(e.ctx, info.ID); err != nil {
		t.Fatalf("the internal root could not dismiss: %v", err)
	}
}

// A running turn keeps the session open: dismissal never stops work.
func TestDismissRefusesRunningSession(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.send(info.ID, "RESPOND: busy SLEEP:500", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(info.ID, StatusRunning)
	ownerDismissEnv(t)
	if _, err := e.m.DismissSession(e.ctx, info.ID); err == nil {
		t.Fatal("dismissal closed a running session")
	}
	if turn, _ := e.m.Store.ReadTurn(info.ID, 2); turn.StopAsked {
		t.Fatal("dismissal stopped the running turn")
	}
	_, _ = e.m.Stop(e.ctx, info.ID)
}

// A reservation that expired during the check no longer fenced off new work,
// so Finish must not commit: a goal assigned after expiry stays with an open
// session.
func TestFinishRefusesAfterItsReservationExpired(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	e.doneGoals()
	var assignErr error
	e.m.checkpoint = func(stage string) {
		if stage != "finish-checked" {
			return
		}
		if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
			meta.Closing.At = time.Now().Add(-sessionlife.ClosingLimit - time.Minute)
		}); err != nil {
			t.Fatal(err)
		}
		_, assignErr = e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "late work", Holders: []string{info.ID}})
	}
	if _, err := e.m.Finish(e.ctx, info.ID); err == nil {
		t.Fatal("finish committed after its reservation expired")
	}
	if assignErr != nil {
		t.Fatalf("expired reservation still refused new work: %v", assignErr)
	}
	if got, _ := e.m.Store.Info(info.ID); !got.Finished.IsZero() || got.Closing.Active() {
		t.Fatalf("session after the refused finish = %+v", got.Meta)
	}
}

func TestSessionStatusIsTheLifecycleContract(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	raw := []byte(fmt.Sprintf(`{"id":%q,"roomId":%q,"room":%q,"status":"dormant"}`, id, e.room.ID, e.room.Name))
	writeRoomScanFixture(t, e, id, raw)
	info, err := e.m.Store.Info(id)
	if err != nil || info.Status != StatusDormant {
		t.Fatalf("status-only dormant: %+v %v", info, err)
	}
	if _, err := e.m.Store.UpdateMeta(id, func(meta *Meta) {
		meta.Status = StatusIdle
		meta.Finished = time.Now()
	}); err != nil {
		t.Fatal(err)
	}
	info, err = e.m.Store.Info(id)
	if err != nil || info.Status != StatusIdle {
		t.Fatalf("timestamp overrode explicit status: %+v %v", info, err)
	}

}

func TestLegacyFinishedSessionReadsAsDormant(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	raw := []byte(fmt.Sprintf(`{"id":%q,"roomId":%q,"room":%q,"finished":"2026-01-01T00:00:00Z"}`, id, e.room.ID, e.room.Name))
	writeRoomScanFixture(t, e, id, raw)
	info, err := e.m.Store.Info(id)
	if err != nil || info.Status != StatusDormant {
		t.Fatalf("legacy finished: %+v %v", info, err)
	}
}

func TestSessionTurnStatusTransitions(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	raw := []byte(fmt.Sprintf(`{"id":%q,"roomId":%q,"room":%q}`, id, e.room.ID, e.room.Name))
	writeRoomScanFixture(t, e, id, raw)
	check := func(want string) {
		t.Helper()
		info, err := e.m.Store.Info(id)
		if err != nil || info.Status != want {
			t.Fatalf("status = %s, want %s: %v", info.Status, want, err)
		}
	}
	check(StatusIdle)
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Store.UpdateMeta(id, func(m *Meta) { m.Turns = 1 }); err != nil {
		t.Fatal(err)
	}
	lock, err := e.m.Store.tryTurnLock(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, state := range []string{TurnQueued, TurnRunning} {
		if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: state}); err != nil {
			t.Fatal(err)
		}
		check(state)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	check(StatusIdle)
}

func TestClosedPendingTurnStaysClosed(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	raw := []byte(fmt.Sprintf(`{"id":%q,"roomId":%q,"room":%q,"closed":"2026-01-01T00:00:00Z","turns":1}`, id, e.room.ID, e.room.Name))
	writeRoomScanFixture(t, e, id, raw)
	dir, err := e.m.Store.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Store.writeTurn(id, Turn{N: 1, Status: TurnQueued, RoomHeld: true}); err != nil {
		t.Fatal(err)
	}
	info, err := e.m.Store.Info(id)
	if err != nil || info.Status != StatusClosed {
		t.Fatalf("closed pending turn changed session status: %+v %v", info, err)
	}
}

func TestGoalAssignedAfterFinishReturnsSessionToIdle(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	g, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "Continue work", Holders: []string{info.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Store.Info(info.ID)
	if err != nil || got.Status != StatusIdle || !got.Finished.IsZero() {
		t.Fatalf("goal owner = %+v, %v", got, err)
	}
	if _, err = e.m.Finish(e.ctx, info.ID); err == nil || !strings.Contains(err.Error(), "holds goal "+g.ID) {
		t.Fatalf("finish with active goal = %v", err)
	}
}

func TestDormantWakeReturnsToDormantUnlessItTakesWork(t *testing.T) {
	for _, which := range []string{"unclaimed", "goal", "originally-idle"} {
		t.Run(which, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			if which != "originally-idle" {
				if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
					t.Fatal(err)
				}
			}
			if which == "goal" {
				e.m.checkpoint = func(stage string) {
					if stage == "turn-finished" {
						if _, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: "New work", Holders: []string{info.ID}}); err != nil {
							t.Error(err)
						}
					}
				}
			}
			if _, err := e.send(info.ID, "SLEEP:1 RESPOND: handled", WakeOptions{}); err != nil {
				t.Fatal(err)
			}
			got, turn := e.wait(info.ID, 2)
			want := StatusIdle
			if which == "unclaimed" {
				want = StatusDormant
			}
			if turn.Status != TurnOK || got.Status != want {
				t.Fatalf("turn=%s status=%s, want %s", turn.Status, got.Status, want)
			}
			if got.DormantWake {
				t.Fatal("completed wake retained its dormant origin")
			}
		})
	}
}

func TestDormantWakeWaitsForQueuedInput(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	posted := false
	e.m.checkpoint = func(stage string) {
		if stage == "turn-finished" && !posted {
			posted = true
			if _, err := e.m.rooms().Post(info.RoomID, room.PostInput{Wake: true, From: info.Room, To: []string{info.Address()}, Text: "SLEEP:1 RESPOND: queued"}); err != nil {
				t.Error(err)
			}
		}
	}
	if _, err := e.send(info.ID, "SLEEP:1 RESPOND: first", WakeOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.wait(info.ID, 2)
	if got.Status != StatusIdle || !got.DormantWake {
		t.Fatalf("queued input lost its active wake chain: status=%s dormantWake=%v", got.Status, got.DormantWake)
	}
	if err := e.m.TickSessionTargets(e.ctx, []string{info.ID}); err != nil {
		t.Fatal(err)
	}
	got, turn := e.wait(info.ID, 3)
	if turn.Status != TurnOK || got.Status != StatusDormant {
		t.Fatalf("queued turn=%s status=%s", turn.Status, got.Status)
	}
}

// A session finishes itself from inside a Dyna shell job; that job ends with
// the command, so it is not unfinished work. Any other live job still is.
func TestFinishIgnoresTheShellJobThatRunsIt(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: idle")
	info, _ = e.wait(info.ID, 1)
	e.doneGoals()
	st := shells.Store{Root: filepath.Join(filepath.Dir(e.m.Store.root()), "shells")}
	own, other := "sh_20261013-184235-2b655443", "sh_20261013-184236-2b655444"
	for _, job := range []string{own, other} {
		if err := st.Save(shells.Meta{ID: job, Owner: info.ID, Supervisor: os.Getpid(), Cmd: []string{"true"}, Started: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(shells.JobEnv, own)
	_, err := e.m.Finish(e.ctx, info.ID)
	if err == nil || strings.Contains(err.Error(), own) || !strings.Contains(err.Error(), "live shell job "+other) {
		t.Fatalf("finish error = %v, want only the other job", err)
	}
	if err := st.Save(shells.Meta{ID: other, Owner: info.ID, Cmd: []string{"true"}, Started: time.Now()}, &shells.Exit{Ended: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Finish(e.ctx, info.ID); err != nil {
		t.Fatalf("finish from inside its own job: %v", err)
	}
}
