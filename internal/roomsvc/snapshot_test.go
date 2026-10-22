package roomsvc

import (
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

// Room watches read the shared snapshot: a closed session's record edit shows
// on the next tick, not only on the next full scan, and the watch fills the
// cache that requests use.
func TestMemberWatchListsFromTheSharedSnapshot(t *testing.T) {
	e := newEnv(t)
	rec := e.room("watch-snapshot")
	info := e.start(rec, "done", nil)
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Closed = time.Now() }); err != nil {
		t.Fatal(err)
	}
	w := newMemberWatch()
	if _, err := w.list(e.svc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	sessions, err := w.list(e.svc)
	if err != nil {
		t.Fatal(err)
	}
	if w.full {
		t.Fatal("the second scan is a full scan; the test needs a partial one")
	}
	found := false
	for _, s := range sessions {
		if s.ID == info.ID {
			found = true
			if s.Profile != "changed" {
				t.Fatalf("closed session profile %q, want the edited %q", s.Profile, "changed")
			}
		}
	}
	if !found {
		t.Fatalf("watch listing lacks %s", info.ID)
	}
	if rev, _ := e.svc.sessions().Revision(); rev == "" || e.svc.snap.rev != rev {
		t.Fatalf("watch did not fill the shared snapshot: cache %q, store %q", e.svc.snap.rev, rev)
	}
}

// A room's avatar book is built once per session revision: a second request
// with no record write reuses it, and a new session invalidates it.
func TestAvatarBookIsReusedUntilASessionRecordChanges(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	first := e.start(rec, "lead", nil)
	book := e.svc.avatars(rec.ID)
	if book.byID[first.ID] == "" {
		t.Fatalf("book has no avatar for %s: %v", first.ID, book.byID)
	}
	again := e.svc.avatars(rec.ID)
	if reflect.ValueOf(again.byID).Pointer() != reflect.ValueOf(book.byID).Pointer() {
		t.Fatal("the avatar book was rebuilt although no session record changed")
	}
	second := e.start(rec, "helper", nil)
	fresh := e.svc.avatars(rec.ID)
	if reflect.ValueOf(fresh.byID).Pointer() == reflect.ValueOf(book.byID).Pointer() || fresh.byID[second.ID] == "" {
		t.Fatalf("a new session did not invalidate the book: %v", fresh.byID)
	}
	if fresh.byID[first.ID] != book.byID[first.ID] {
		t.Fatalf("an existing avatar moved: %q became %q", book.byID[first.ID], fresh.byID[first.ID])
	}
}

// The cached snapshot still reports each open session's current status,
// which can change with no record write.
func TestSnapshotRefreshesOpenSessionsOnEveryCall(t *testing.T) {
	e := newEnv(t)
	rec := e.room("beta")
	lead := e.start(rec, "lead", nil)
	all, rev := e.svc.snapshot()
	if rev == "" {
		t.Fatal("no session revision; the snapshot is not cached")
	}
	open, err := e.svc.sessions().ListSnapshot(false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, info := range open {
		want[info.ID] = string(info.Status)
	}
	again, rev2 := e.svc.snapshot()
	if rev2 != rev || len(again) != len(all) {
		t.Fatalf("second snapshot: rev %q (was %q), %d sessions (was %d)", rev2, rev, len(again), len(all))
	}
	for _, info := range again {
		if status, ok := want[info.ID]; ok && string(info.Status) != status {
			t.Fatalf("session %s status %q, want the live %q", info.ID, info.Status, status)
		}
	}
	if _, ok := want[lead.ID]; !ok {
		t.Fatalf("open listing lacks %s", lead.ID)
	}
}
