package view_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

const ordinaryMemberID = "ses_20261028-120000-00000001"

// Stored member and room revisions must refresh live observations. Observations
// that cannot change a PR row must not rebuild every row in every room.
func TestPRScanSkipsUnchangedReviewFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *env, rec room.Record, i int)
	}{
		{name: "member", change: func(e *env, _ room.Record, i int) {
			if _, err := e.mgr.Store.UpdateMeta(ordinaryMemberID, func(meta *subagent.Meta) { meta.Name = strconv.Itoa(i) }); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "room", change: func(e *env, rec room.Record, i int) {
			if _, _, err := e.svc.Notify.Rooms.SetSeat(e.ctx, rec.ID, room.Seat{External: i%2 == 0}, nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("unchanged-review-facts")
			recordtest.Write(t, e.mgr.Store.Root, ordinaryMemberID, subagent.Meta{ID: ordinaryMemberID, RoomID: rec.ID, Room: rec.Name, Role: "author"})
			testfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
				"one": {Key: "one", Kind: "github", RoomID: rec.ID, State: "open", Watching: true, CI: "passed"},
				"two": {Key: "two", Kind: "github", RoomID: rec.ID, State: "open", Watching: true, CI: "passed"},
			}})
			signatures, work, version := map[string]string{}, map[string]string{}, ""
			events := 0
			scan := func() {
				e.svc.PRs.Scan(signatures, work, &version, false, func(string, string, *listchange.Change, bool) { events++ })
			}
			scan()
			events = 0
			before := version
			facts, rows := view.CountProjectionFacts(t), view.CountRows(t)
			inputs, queries := view.CountWorkerInputJoins(t), view.CountChangedKeyQueries(t)
			for i := range 3 {
				tc.change(e, rec, i)
				scan()
			}
			if *facts != 3 {
				t.Fatalf("stored revisions refreshed %d observations; want 3", *facts)
			}
			if *rows != 0 || *inputs != 0 || *queries != 0 || events != 0 || version != before {
				t.Fatalf("unchanged review facts: rows=%d inputs=%d queries=%d events=%d version %q -> %q; want no PR work", *rows, *inputs, *queries, events, before, version)
			}
		})
	}
}

type memberWatchContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *memberWatchContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.started) })
	return c.Context.Done()
}

// The room watcher must retain its PR cursor across a member event. A later
// external PR write then proves both the saved cursor and its delta query work.
func TestRoomWatchMemberEventKeepsPRDelta(t *testing.T) {
	e := newEnv(t)
	rec := e.room("watch-member-pr-delta")
	recordtest.Write(t, e.mgr.Store.Root, ordinaryMemberID, subagent.Meta{ID: ordinaryMemberID, RoomID: rec.ID, Room: rec.Name, Role: "author"})
	root := e.svc.PRs.Watcher().Store.Root
	testfixture.SaveState(t, root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"one": {Key: "one", Kind: "github", RoomID: rec.ID, Title: "before", State: "open", Watching: true, CI: "passed"},
		"two": {Key: "two", Kind: "github", RoomID: rec.ID, State: "open", Watching: true, CI: "passed"},
	}})
	base, cancel := context.WithCancel(e.ctx)
	ctx := &memberWatchContext{Context: base, started: make(chan struct{})}
	events, stopped := make(chan roomsvc.Event, 16), make(chan struct{})
	go func() {
		defer close(stopped)
		// Explicit wake channels drive every scan; the real ticker never fires.
		e.svc.Watch(ctx, time.Hour, func(event roomsvc.Event) {
			select {
			case events <- event:
			case <-base.Done():
			}
		})
	}()
	defer func() {
		cancel()
		<-stopped
	}()
	select {
	case <-ctx.started:
	case <-stopped:
		t.Fatal("watch stopped during its initial scan")
	}
	rows := view.CountRows(t)
	scans := make(chan struct{}, 1)
	e.svc.AfterWakeScan = func() { scans <- struct{}{} }
	if _, err := e.mgr.Store.UpdateMeta(ordinaryMemberID, func(meta *subagent.Meta) { meta.Name = "changed" }); err != nil {
		t.Fatal(err)
	}
	assertEvent := func(kind, id string) {
		t.Helper()
		for {
			select {
			case event := <-events:
				if event.Kind == kind && event.Change != nil && event.Change.ID == id {
					return
				}
			default:
				t.Fatalf("completed scan did not emit %s event %s", kind, id)
			}
		}
	}
	e.svc.SessionChanged(ordinaryMemberID)
	<-scans
	assertEvent("members", ordinaryMemberID)
	if err := state.UpdateRecords(t.Context(), root, []string{"one"}, func(st *state.State) error {
		st.Records["one"].Title = "after"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.svc.PRsChanged()
	<-scans
	assertEvent("changes", "one")
	if *rows != 1 {
		t.Fatalf("member event and one external PR write built %d rows; want 1", *rows)
	}
}

func BenchmarkPRScanAfterMemberEvent(b *testing.B) {
	e := newEnv(b)
	rooms := []room.Record{e.room("one"), e.room("two"), e.room("three"), e.room("four")}
	recordtest.Write(b, e.mgr.Store.Root, ordinaryMemberID, subagent.Meta{ID: ordinaryMemberID, RoomID: rooms[0].ID, Room: rooms[0].Name, Role: "author"})
	st := watch.State{Version: 1, Records: map[string]*watch.Record{}}
	created := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, rec := range rooms {
		for i := range 100 {
			key := fmt.Sprintf("github.com/owner/repo#%s-%d", rec.Name, i)
			st.Records[key] = &watch.Record{Key: key, Kind: "github", RoomID: rec.ID, Number: i + 1, Title: "Representative pull request " + key,
				State: "open", Watching: true, CI: "passed", Head: "head-" + key, Created: created.Add(time.Duration(i) * time.Second)}
		}
	}
	testfixture.SaveState(b, e.svc.PRs.Watcher().Store.Root, st)
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.PRs.Scan(signatures, work, &version, true, func(string, string, *listchange.Change, bool) {})
	rows, facts := view.CountRows(b), view.CountProjectionFacts(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if _, err := e.mgr.Store.UpdateMeta(ordinaryMemberID, func(meta *subagent.Meta) { meta.Name = strconv.Itoa(i) }); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		e.svc.PRs.Scan(signatures, work, &version, false, func(string, string, *listchange.Change, bool) {})
	}
	b.StopTimer()
	b.ReportMetric(float64(*rows)/float64(b.N), "rows/op")
	b.ReportMetric(float64(*facts)/float64(b.N), "observations/op")
}
