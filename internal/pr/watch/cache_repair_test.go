package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// TestCacheRepairPostsOneTypedMachineEvent checks the adapter between a run's
// repair fact and the room: one quiet machine event with the machine name and
// human cache kinds, posted to the room whose CI triggered the repair.
func TestCacheRepairPostsOneTypedMachineEvent(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "cache-repair"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1700000000000)
	s := &Service{Notify: notify.New(rooms, subagent.NewManager()), Now: func() time.Time { return at }}
	s.CacheRepairReporter(rec.ID)(t.Context(), localci.CacheRepair{
		Node:  "node-a",
		Kinds: []localci.CacheKind{localci.CacheGoBuild, localci.CacheGoMod},
	})
	posts, _, err := rooms.ReadAll(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, post := range posts {
		if post.Kind != string(room.FamilyMachine) || post.Notice.Kind != string(room.TypeCacheRepaired) {
			continue
		}
		found = true
		if post.Wake {
			t.Fatalf("cache repair woke a session: %+v", post)
		}
		ev, ok := post.MachineCache()
		if !ok || ev.Name != "node-a" || len(ev.Caches) != 2 || ev.Caches[0] != "Go build cache" || ev.Caches[1] != "Go module cache" ||
			ev.Summary != "Go build cache and Go module cache" || ev.AtMS != int(at.UnixMilli()) {
			t.Fatalf("stored event = %+v, %v", ev, ok)
		}
	}
	if !found {
		t.Fatalf("no cache repair event: %+v", posts)
	}
}
