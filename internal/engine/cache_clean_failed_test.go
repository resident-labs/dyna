package engine

import (
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// TestCacheCleanFailurePostsOneTypedAlert checks the boundary between a
// machine cleanup and its rooms. An hourly retry of the same path and reason
// reuses the alert, but an identical failure after a success starts a new one.
func TestCacheCleanFailurePostsOneTypedAlert(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "cache-clean"})
	if err != nil {
		t.Fatal(err)
	}
	svc := notify.New(rooms, subagent.NewManager())
	report := cacheCleanFailedReporter(svc)
	node := localci.Node{Name: "node-a", Rooms: []string{rec.ID}}
	failure := localci.CacheCleanError{
		Node:   node.Name,
		Path:   "/var/tmp/dyna-local-ci/runs/old/web/node_modules",
		Reason: "permission denied",
	}
	for range 2 {
		if err := report(t.Context(), node, failure); err != nil {
			t.Fatal(err)
		}
	}
	posts, _, err := rooms.ReadAll(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var alerts []room.Message
	for _, post := range posts {
		if post.Kind == string(room.FamilyMachine) && post.Notice.Kind == string(room.TypeCacheCleanFailed) {
			alerts = append(alerts, post)
		}
	}
	if len(alerts) != 1 {
		t.Fatalf("stored %d cleanup alerts, want one: %+v", len(alerts), posts)
	}
	if !alerts[0].Wake {
		t.Fatal("a new cleanup failure did not wake the room's root")
	}
	ev, ok := alerts[0].MachineCacheCleanFailed()
	if !ok || ev.Name != node.Name || ev.Path != failure.Path || ev.Reason != failure.Reason {
		t.Fatalf("stored cleanup event = %+v, %v", ev, ok)
	}

	// The successful pass changes the generation. The same path and reason in
	// the next failure period must not reuse the old alert.
	failure.LastSuccessMS = 1700000000000
	if err := report(t.Context(), node, failure); err != nil {
		t.Fatal(err)
	}
	posts, _, err = rooms.ReadAll(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	alerts = alerts[:0]
	for _, post := range posts {
		if post.Kind == string(room.FamilyMachine) && post.Notice.Kind == string(room.TypeCacheCleanFailed) {
			alerts = append(alerts, post)
		}
	}
	if len(alerts) != 2 {
		t.Fatalf("stored %d cleanup alerts after a new incident, want two: %+v", len(alerts), posts)
	}
	ev, ok = alerts[1].MachineCacheCleanFailed()
	if !ok || ev.LastSuccessMS != failure.LastSuccessMS {
		t.Fatalf("new incident payload = %+v, %v", ev, ok)
	}
}
