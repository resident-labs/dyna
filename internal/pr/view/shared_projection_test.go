package view_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

func TestCommittedPRViewSharedByFiveSubscribers(t *testing.T) {
	e := newEnv(t)
	rec := e.room("shared-pr")
	root := e.svc.PRs.Watcher().Store.Root
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"repo#1": {Key: "repo#1", Kind: "github", RoomID: rec.ID, Title: "before", State: "open", Watching: true,
			Checks:   []watch.CheckSummary{{Name: "go", State: "success"}},
			Findings: []watch.Finding{{ID: "finding", Replies: []state.FindingReply{{Message: "original"}}}}},
	}}
	testfixture.SaveState(t, root, st)
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.PRs.Scan(signatures, work, &version, true, func(string, string, *listchange.Change, bool) {})
	copies, rows := view.CountProjectionCopies(t), view.CountRows(t)
	events := hub.New(16)
	var subscribers []<-chan hub.Event
	for range 5 {
		stream, _, close := events.Subscribe()
		t.Cleanup(close)
		subscribers = append(subscribers, stream)
	}
	st.Records["repo#1"].Title = "committed"
	testfixture.SaveState(t, root, st)
	e.svc.PRs.Scan(signatures, work, &version, false, func(kind, roomID string, change *listchange.Change, _ bool) {
		if kind == "changes" && change != nil {
			events.Publish(hub.Event{Topic: kind, Room: roomID, Key: change.ID})
		}
	})
	for _, stream := range subscribers {
		select {
		case event := <-stream:
			if event.Room != rec.ID || event.Key != "repo#1" {
				t.Fatalf("subscriber event = %+v", event)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("subscriber missed the committed PR event")
		}
		page, err := e.svc.PRs.Changes(rec.ID, "", 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].Title != "committed" || page.Items[0].Checks[0].Name != "go" {
			t.Fatalf("subscriber page = %+v, %v", page, err)
		}
		detail, err := e.svc.PRs.ChangeDetail(rec.ID, "repo#1")
		if err != nil || len(detail.Findings) != 1 || detail.Findings[0].Replies[0].Message != "original" {
			t.Fatalf("shared detail changed: %+v, %v", detail, err)
		}
		detail.Findings[0].Replies[0].Message = "caller mutation"
		// A Go caller can mutate its reply without changing the next reply.
		page.Items[0].Title = "caller mutation"
		page.Items[0].Checks[0].Name = "caller mutation"
	}
	if *copies != 1 || *rows != 1 {
		t.Fatalf("one PR write plus five requests made %d state copies and built %d rows; want one of each", *copies, *rows)
	}
}

func TestSharedPRViewDoesNotCrossRoomOrSeatChanges(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("a"), e.room("b")
	root := e.svc.PRs.Watcher().Store.Root
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"repo#1": {Key: "repo#1", Kind: "github", RoomID: a.ID, Title: "a", State: "open"},
		"repo#2": {Key: "repo#2", Kind: "github", RoomID: b.ID, Title: "b", State: "open"},
	}}
	testfixture.SaveState(t, root, st)
	for _, roomID := range []string{a.ID, b.ID, a.ID} {
		page, err := e.svc.PRs.Changes(roomID, "", 10)
		if err != nil || len(page.Items) != 1 || page.Items[0].RoomID != roomID {
			t.Fatalf("room %s got %+v, %v", roomID, page, err)
		}
	}
	if _, err := e.svc.PRs.ChangeDetail(b.ID, "repo#1"); err == nil {
		t.Fatal("shared projection crossed the room boundary")
	}
	if _, _, err := e.svc.Notify.Rooms.SetSeat(e.ctx, a.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PRs.ChangeDetail(b.ID, "repo#1"); err == nil {
		t.Fatal("external seat widened the room boundary")
	}
}

func TestSharedPRViewRefreshesPauseAndUnkeyedReviewerExit(t *testing.T) {
	e := newEnv(t)
	rec := e.room("reviewer-exit")
	var runners sync.WaitGroup
	e.mgr.Spawner = subagent.InProcessSpawner{Ctx: e.ctx, Runners: &runners}
	reviewer, _, err := e.mgr.Start(e.ctx, subagent.StartOptions{Room: rec.Name, Name: "reviewer", Dir: t.TempDir(), Profile: "mock", Goal: "Finish the fixture turn", Task: "RESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { runners.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the fixture runner did not finish")
	}
	if _, err := e.mgr.Store.UpdateMeta(reviewer.ID, func(meta *subagent.Meta) { meta.Role = "reviewer" }); err != nil {
		t.Fatal(err)
	}
	dir, err := e.mgr.Store.Dir(reviewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	unlock, err := store.LockContext(ctx, filepath.Join(dir, "turn.lock"))
	if err != nil {
		t.Fatal(err)
	}
	unlock = sync.OnceFunc(unlock)
	t.Cleanup(unlock)
	if _, err := e.mgr.Store.UpdateTurn(reviewer.ID, 1, func(turn *subagent.Turn) { turn.Status = subagent.TurnRunning }); err != nil {
		t.Fatal(err)
	}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"repo#1": {Key: "repo#1", Kind: "github", RoomID: rec.ID, State: "open", Watching: true, Head: "head", RoundHead: "head", CI: "passed", Reviewer: reviewer.ID},
	}}
	testfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	assert := func(want string) {
		t.Helper()
		page, err := e.svc.PRs.Changes(rec.ID, "", 0)
		if err != nil || len(page.Items) != 1 || page.Items[0].ReviewState != want {
			t.Fatalf("review state = %+v, %v; want %s", page, err, want)
		}
	}
	assert("reviewing")
	copies := view.CountProjectionCopies(t)
	revision, err := e.mgr.Store.MembersRevision()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	e.svc.SessionsChanged() // A real lock release changes no stored row.
	if now, _ := e.mgr.Store.MembersRevision(); now != revision {
		t.Fatal("the exit fixture wrote a row")
	}
	assert("queued")
	for _, paused := range []bool{true, false} {
		if _, err := e.svc.Notify.Rooms.SetSessionsPaused(e.ctx, rec.ID, paused); err != nil {
			t.Fatal(err)
		}
		want := "queued"
		if paused {
			want = "blocked"
		}
		assert(want)
	}
	if *copies != 0 {
		t.Fatalf("session and pause changes copied PR state %d times; want none", *copies)
	}
}

func TestSharedPRScanSkipsChangedKeyQueryForUnchangedRevision(t *testing.T) {
	e := newEnv(t)
	rec := e.room("unchanged-pr-revision")
	root := e.svc.PRs.Watcher().Store.Root
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"repo#1": {Key: "repo#1", Kind: "github", RoomID: rec.ID, State: "open", Watching: true},
	}})
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	scan := func() {
		e.svc.PRs.Scan(signatures, work, &version, false, func(string, string, *listchange.Change, bool) {})
	}
	scan()
	before := version
	queries := view.CountChangedKeyQueries(t)
	for range 5 {
		scan()
	}
	// A room fact requires a new projection, but no PR row was committed.
	if _, _, err := e.svc.Notify.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	scan()
	if *queries != 0 || version != before {
		t.Fatalf("unchanged PR revision: queries=%d, version %q -> %q; want no delta query", *queries, before, version)
	}
}

func TestProjectionCopiesOnlyChangedRecords(t *testing.T) {
	e := newEnv(t)
	rec := e.room("changed-rows")
	root := e.svc.PRs.Watcher().Store.Root
	st := watch.State{Version: 1, Records: map[string]*watch.Record{}}
	for i := range 20 {
		key := fmt.Sprintf("repo#%d", i)
		st.Records[key] = &watch.Record{Key: key, Kind: "github", RoomID: rec.ID, Title: "before", State: "open", Watching: true,
			Findings: []state.Finding{{ID: "finding", Replies: []state.FindingReply{{Message: "original"}}}}}
	}
	testfixture.SaveState(t, root, st)
	if _, err := e.svc.PRs.Changes(rec.ID, "", 100); err != nil {
		t.Fatal(err)
	}
	copies := view.CountProjectionCopies(t)
	st.Records["repo#0"].Title = "after"
	testfixture.SaveState(t, root, st)
	for range 5 {
		detail, err := e.svc.PRs.ChangeDetail(rec.ID, "repo#0")
		if err != nil || detail.Title != "after" {
			t.Fatalf("changed detail = %+v, %v", detail, err)
		}
		detail.Findings[0].Replies[0].Message = "caller mutation"
		unchanged, err := e.svc.PRs.ChangeDetail(rec.ID, "repo#1")
		if err != nil || unchanged.Title != "before" || unchanged.Findings[0].Replies[0].Message != "original" {
			t.Fatalf("unchanged detail = %+v, %v", unchanged, err)
		}
		unchanged.Findings[0].Replies[0].Message = "caller mutation"
	}
	if *copies != 1 {
		t.Fatalf("one changed PR copied %d records; want 1", *copies)
	}
}
