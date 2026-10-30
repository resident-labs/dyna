package roomsvc

import (
	"dyna-agent/internal/database"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"path/filepath"
	"testing"
)

func BenchmarkWarmUnread(b *testing.B) {
	dir := b.TempDir()
	rooms := &room.Store{Root: filepath.Join(dir, "rooms")}
	mgr := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(dir, "sessions")}}
	svc := New(notify.New(rooms, mgr))
	if _, err := rooms.Create(room.CreateInput{Name: "bench", CreatedBy: "test"}); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { database.Close(dir) })
	for i := 0; i < b.N; i++ {
		if _, err := svc.Rooms(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N-svc.unreadCache.Loads())/float64(b.N), "hits/read")
}
