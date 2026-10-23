package roomsvc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// Five subscribers receive one committed member event, then read the same
// view. Count the real list and Inspect operations, not the number of dirty IDs.
func TestCommittedMemberViewSharedByFiveSubscribers(t *testing.T) {
	e, rec, sessions, w := seedTargetedMembers(t)
	a := sessions[0]
	events := hub.New(16)
	var subscribers []<-chan hub.Event
	for range 5 {
		stream, _, close := events.Subscribe()
		t.Cleanup(close)
		subscribers = append(subscribers, stream)
	}
	lists, reads, trees, recordReads, rosterCopies := 0, 0, 0, 0, 0
	read, copyRoster := readSnapshotSession, copyMemberRoster
	readSnapshotSession = func(readRecord func(string) (subagent.Info, error), id string) (subagent.Info, error) {
		recordReads++
		return read(readRecord, id)
	}
	copyMemberRoster = func(all []subagent.Info) []subagent.Info {
		rosterCopies++
		return copyRoster(all)
	}
	t.Cleanup(func() { readSnapshotSession, copyMemberRoster = read, copyRoster })
	build := buildMemberItems
	buildMemberItems = func(s *Service, rec room.Record, all []subagent.Info, rev string, closed bool) []Member {
		trees++
		return build(s, rec, all, rev, closed)
	}
	t.Cleanup(func() { buildMemberItems = build })
	list := listSessionSnapshot
	listSessionSnapshot = func(store *subagent.Store) ([]subagent.Info, error) {
		lists++
		return list(store)
	}
	t.Cleanup(func() { listSessionSnapshot = list })
	if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) { meta.Profile = "committed" }); err != nil {
		t.Fatal(err)
	}
	e.svc.SessionChanged(a.ID)
	e.svc.scanDirtyMembers(w, func(event Event) {
		if event.Kind == "members" {
			events.Publish(hub.Event{Topic: event.Kind, Room: event.RoomID, Key: event.Change.ID})
		}
	})
	for _, stream := range subscribers {
		select {
		case event := <-stream:
			if event.Room != rec.ID || event.Key != a.ID {
				t.Fatalf("subscriber event = %+v", event)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("subscriber missed the committed member event")
		}
		all, rev, err := e.svc.readSnapshot(func(id string) (subagent.Info, error) {
			reads++
			return e.mgr.Store.Inspect(id)
		})
		if err != nil {
			t.Fatal(err)
		}
		members := e.svc.membersOf(rec, all, rev, true)
		found := false
		for _, member := range members {
			found = found || member.ID == a.ID && member.Profile == "committed"
		}
		if len(members) != 3 || !found {
			t.Fatalf("subscriber lost the committed full roster: %+v", members)
		}
	}
	if recordReads != 1 || rosterCopies != 1 {
		t.Errorf("one member write plus five requests made %d record reads and %d roster copies; want one of each", recordReads, rosterCopies)
	}
	if trees != 1 {
		t.Errorf("one member write plus five requests built %d trees; want one", trees)
	}
	if lists != 0 || reads != 0 {
		t.Fatalf("five subscribers repeated %d lists and %d Inspect calls; want the committed watch view without either", lists, reads)
	}
}

// A service session is outside the hierarchy, so a member view never shows one,
// whatever its ancestor's stored flags say. Requests and targeted watch scans
// must still agree on the records they read.
func TestCommittedMemberViewLeavesServicesOut(t *testing.T) {
	for _, requestFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("request-first=%t", requestFirst), func(t *testing.T) {
			e := newEnv(t)
			rec := e.room("inherited-service")
			parent, child, grandchild, unrelated := subagent.NewID(), subagent.NewID(), subagent.NewID(), subagent.NewID()
			for i, meta := range []subagent.Meta{
				{ID: parent, Name: "parent"},
				{ID: child, Name: "child", ParentSession: parent, Service: true},
				{ID: grandchild, Name: "grandchild", ParentSession: child, Service: true},
				{ID: unrelated, Name: "unrelated"},
			} {
				meta.RoomID, meta.Room, meta.Created = rec.ID, rec.Name, time.Unix(int64(i+1), 0)
				recordtest.Write(t, e.mgr.Store.Root, meta.ID, meta)
			}
			w := newMemberWatch()
			all, err := w.list(e.svc)
			if err != nil {
				t.Fatal(err)
			}
			e.svc.scanMembers(all, []room.Record{rec}, w, nil, true, func(Event) {})
			assertMembers := func() {
				t.Helper()
				members, err := e.svc.Members(rec.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				for _, member := range members {
					if member.ID == child || member.ID == grandchild {
						t.Fatalf("service session is a member: %+v", member)
					}
				}
			}
			assertMembers() // Warm the rendered tree before the ancestor write.
			reads := map[string]int{}
			read := readSnapshotSession
			readSnapshotSession = func(fn func(string) (subagent.Info, error), id string) (subagent.Info, error) {
				reads[id]++
				return read(fn, id)
			}
			t.Cleanup(func() { readSnapshotSession = read })
			for _, change := range []struct {
				service bool
				role    string
			}{{true, ""}, {true, subagent.RoleRoot}, {false, ""}} {
				clear(reads)
				if _, err := e.mgr.Store.UpdateMeta(parent, func(meta *subagent.Meta) {
					meta.Service, meta.Role = change.service, change.role
				}); err != nil {
					t.Fatal(err)
				}
				if requestFirst {
					assertMembers()
				}
				ids := map[string]bool{parent: true}
				fresh, err := w.refresh(e.svc, ids)
				if err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				e.svc.scanMembers(fresh, []room.Record{rec}, w, ids, false, func(event Event) {
					if event.Kind == "members" && event.Change != nil {
						seen[event.Change.ID] = true
					}
				})
				// Ancestor changes must not change descendants' stored flags.
				if seen[child] || seen[grandchild] {
					t.Errorf("ancestor change published unchanged descendant members: %v", seen)
				}
				assertMembers()
				if reads[parent] != 1 || reads[child] != 1 || reads[grandchild] != 1 || reads[unrelated] != 0 {
					t.Errorf("ancestor refresh reads=%v; want each affected record once and no unrelated read", reads)
				}
			}
		})
	}
}

func TestCommittedMemberUpdateDoesNotSortTheWholeRoster(t *testing.T) {
	e, _, sessions, w := seedTargetedMembers(t)
	sorted := 0
	sort := sortMemberSessions
	sortMemberSessions = func(in []subagent.Info) {
		if len(in) > 1 {
			sorted += len(in)
		}
		sort(in)
	}
	t.Cleanup(func() { sortMemberSessions = sort })
	a := sessions[0]
	if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	fresh, err := w.refresh(e.svc, map[string]bool{a.ID: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || fresh[0].Profile != "changed" {
		t.Fatalf("changed member = %+v", fresh)
	}
	if sorted != 0 {
		t.Fatalf("one non-ordering write sorted %d cached records; want none", sorted)
	}
}

func TestSharedMemberViewPreservesUnwokenRecordWrites(t *testing.T) {
	e, _, sessions, w := seedTargetedMembers(t)
	a, b := sessions[0], sessions[1]
	for _, info := range []subagent.Info{b, a} {
		if _, err := e.mgr.Store.UpdateMeta(info.ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
			t.Fatal(err)
		}
	}
	e.svc.SessionChanged(a.ID)
	e.svc.scanDirtyMembers(w, func(Event) {})
	all, _ := e.svc.snapshot()
	found := false
	for _, info := range all {
		if info.ID == b.ID {
			found = info.Profile == "changed"
		}
	}
	if !found {
		t.Fatal("the targeted view hid the unwoken record from the shared snapshot")
	}
}

func TestSharedMemberViewKeepsRoomSeatAndClosedFilteringCurrent(t *testing.T) {
	e, rec, sessions, _ := seedTargetedMembers(t)
	closed := sessions[1]
	if _, err := e.mgr.Store.UpdateMeta(closed.ID, func(meta *subagent.Meta) { meta.Closed = time.Now() }); err != nil {
		t.Fatal(err)
	}
	for _, withClosed := range []bool{true, false, true} {
		members, err := e.svc.Members(rec.ID, withClosed)
		want := 2
		if withClosed {
			want = 3
		}
		if err != nil || len(members) != want {
			t.Fatalf("withClosed=%v: members %+v, %v", withClosed, members, err)
		}
	}
	for _, external := range []bool{true, false} {
		if _, _, err := e.svc.rooms().SetSeat(e.ctx, rec.ID, room.Seat{External: external}, nil); err != nil {
			t.Fatal(err)
		}
		members, err := e.svc.Members(rec.ID, true)
		if err != nil || members[0].Seat == nil || members[0].Seat.External != external {
			t.Fatalf("external=%v: members %+v, %v", external, members, err)
		}
	}
}

func TestCommittedMemberSnapshotsAndRepliesStayIndependent(t *testing.T) {
	e, rec, sessions, w := seedTargetedMembers(t)
	before, _ := e.svc.snapshot()
	a := sessions[0]
	if _, err := e.mgr.Store.UpdateMeta(a.ID, func(meta *subagent.Meta) { meta.Profile = "after" }); err != nil {
		t.Fatal(err)
	}
	e.svc.SessionChanged(a.ID)
	e.svc.scanDirtyMembers(w, func(Event) {})
	after, _ := e.svc.snapshot()
	if before[0].Profile == "after" || after[0].Profile != "after" {
		t.Fatal("a committed refresh changed a retained snapshot")
	}
	first, err := e.svc.Members(rec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Seat.External = true
	target := -1
	for i := range first {
		if first[i].ID == a.ID {
			target = i
		}
	}
	if target < 0 {
		t.Fatal("the public roster lost the changed session")
	}
	first[target].Profile = "caller mutation"
	second, err := e.svc.Members(rec.ID, true)
	if err != nil || second[0].Seat.External || second[target].Profile != "after" {
		t.Fatalf("caller changed the shared member view: %+v, %v", second, err)
	}
}

func TestSharedMemberSnapshotObservesUnkeyedExitWithoutRepairingRequests(t *testing.T) {
	e := newEnv(t)
	rec := e.room("exit")
	info := e.start(rec, "worker", nil)
	dir, err := e.mgr.Store.Dir(info.ID)
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
	if _, err := e.mgr.Store.UpdateTurn(info.ID, 1, func(turn *subagent.Turn) { turn.Status = subagent.TurnRunning }); err != nil {
		t.Fatal(err)
	}
	w := newMemberWatch()
	if _, err := w.list(e.svc); err != nil {
		t.Fatal(err)
	}
	before, _ := e.svc.snapshot()
	if before[0].Status != subagent.StatusRunning {
		t.Fatalf("held runner = %s", before[0].Status)
	}
	revision, err := e.mgr.Store.MembersRevision()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	e.svc.SessionsChanged() // The pidfd exit path commits no row.
	after, _ := e.svc.snapshot()
	if after[0].Status == subagent.StatusRunning || before[0].Status != subagent.StatusRunning {
		t.Fatal("the exit did not refresh only the new snapshot")
	}
	if next, _ := e.mgr.Store.MembersRevision(); next != revision {
		t.Fatal("the read-only request repaired a turn")
	}
	turn, err := e.mgr.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.Status != subagent.TurnRunning {
		t.Fatalf("request changed the turn: %+v, %v", turn, err)
	}
	if _, err := w.list(e.svc); err != nil {
		t.Fatal(err)
	}
	turn, err = e.mgr.Store.ReadTurn(info.ID, 1)
	if err != nil || turn.Status != subagent.TurnError {
		t.Fatalf("watch lost orphan recovery: %+v, %v", turn, err)
	}
}

func TestMembersFallbackReusesCommittedReads(t *testing.T) {
	e, rec, sessions, w := seedTargetedMembers(t)
	reads := 0
	read := readSnapshotSession
	readSnapshotSession = func(fn func(string) (subagent.Info, error), id string) (subagent.Info, error) {
		reads++
		return read(fn, id)
	}
	t.Cleanup(func() { readSnapshotSession = read })
	if _, err := e.mgr.Store.UpdateMeta(sessions[0].ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	e.svc.SessionChanged(sessions[0].ID)
	e.svc.scanDirtyMembers(w, func(Event) {})
	all, err := w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	w.full = true
	e.svc.scanMembers(all, []room.Record{rec}, w, nil, false, func(Event) {})
	if reads != 1 {
		t.Fatalf("one write and its fallback scan read %d records; want one", reads)
	}
}

func TestSharedMemberReadFailureKeepsCoverage(t *testing.T) {
	e, _, sessions, _ := seedTargetedMembers(t)
	before := e.svc.snap.view
	if _, err := e.mgr.Store.UpdateMeta(sessions[0].ID, func(meta *subagent.Meta) { meta.Profile = "retry" }); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("record read failed")
	if _, err := e.svc.readRoster(func(string) (subagent.Info, error) { return subagent.Info{}, injected }, false); !errors.Is(err, injected) {
		t.Fatalf("failed read = %v", err)
	}
	if e.svc.snap.view != before {
		t.Fatal("failed read advanced shared coverage")
	}
	after, err := e.svc.readRoster(e.mgr.Store.Inspect, false)
	if err != nil || after.revision == before.revision || after.all[0].Profile != "retry" || before.all[0].Profile == "retry" {
		t.Fatalf("retry failed or changed the retained view: %+v, %v", after, err)
	}
}

func TestTargetedMemberCoverageReusesItsCapturedCursor(t *testing.T) {
	e, _, sessions, w := seedTargetedMembers(t)
	queries := 0
	read := readMemberChanges
	readMemberChanges = func(store *subagent.Store, since string) (string, []string, bool, error) {
		queries++
		return read(store, since)
	}
	t.Cleanup(func() { readMemberChanges = read })
	if _, err := e.mgr.Store.UpdateMeta(sessions[0].ID, func(meta *subagent.Meta) { meta.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	if _, err := w.refresh(e.svc, map[string]bool{sessions[0].ID: true}); err != nil {
		t.Fatal(err)
	}
	if queries != 1 || w.view == nil {
		t.Fatalf("targeted refresh: cursor reads=%d, shared=%v; want one read and full coverage", queries, w.view != nil)
	}
}
