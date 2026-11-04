package engine

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// lowDisk reports a 1000 GiB disk with free GiB left, so the floor is 100 GiB.
func lowDisk(t *testing.T, s *Service, ctx context.Context, free int64) {
	t.Helper()
	free *= box.GiB
	if err := s.store.RecordHostHeartbeat(ctx, "host", true, &free, 1000*box.GiB, s.now()); err != nil {
		t.Fatal(err)
	}
}

// holdLow keeps a host at free GiB for as long as eviction asks.
func holdLow(t *testing.T, s *Service, ctx context.Context, free int64) {
	t.Helper()
	lowDisk(t, s, ctx, free)
	later := s.now().Add(diskLowWait)
	s.now = func() time.Time { return later }
	lowDisk(t, s, ctx, free)
}

// boxOf names the session's box. The fake host serves the room's root too, so tests must ask about this box and no other.
func boxOf(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) contract.BoxID {
	t.Helper()
	record, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	return record.Box
}

func TestEvictDiskArchivesBeforeRemoving(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	id := boxOf(t, s, ctx, sid)
	passLifecycle(t, s, ctx)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO repos(repo,default_room) VALUES('owner/pr',?)", room); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('owner/pr',1,?,'','')", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("evicted a box from a host with room")
	}
	holdLow(t, s, ctx, 99)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if h.starts != 0 {
		t.Fatalf("eviction started the box %d times: close starts nothing", h.starts)
	}
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{"session.closed"})
	if err != nil || len(events) != 1 || events[0].Body.(contract.SessionClosed).Reason != contract.SessionCloseDisk {
		t.Fatalf("closed events %+v: %v", events, err)
	}
	if !slices.Contains(h.removed, id) {
		t.Fatal("the evicted box stayed")
	}
}
