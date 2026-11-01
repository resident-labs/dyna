package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListReportsFullDisk(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := st.AddHost(ctx, box.Host{Name: "full", State: box.HostReady, DiskBytes: 30 * box.GiB, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	free := int64(0)
	if err := st.RecordHostHeartbeat(ctx, "full", true, &free, 30*box.GiB, now); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, time.Now)
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(got.Hosts) != 2 || !got.Hosts[1].DiskLow {
		t.Fatalf("host list: %+v, error: %v", got, problem)
	}
}

// lowDisk reports a 1000 GiB disk with free GiB left, so the floor is 100 GiB.
func lowDisk(t *testing.T, s *Service, ctx context.Context, free int64) {
	t.Helper()
	free *= box.GiB
	if err := s.store.RecordHostHeartbeat(ctx, "host", true, &free, 1000*box.GiB, s.now()); err != nil {
		t.Fatal(err)
	}
}

func TestFloorHoldsNewBoxesAndWakes(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("box did not stop")
	}
	// 99 GiB clears the old fixed 20 GiB floor, but not 10% of this disk.
	lowDisk(t, s, ctx, 99)
	wakeSession(t, s, ctx, room, sid)
	child, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	for _, id := range []contract.SessionID{sid, child.Session} {
		record, err := s.store.Session(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.store.Box(ctx, record.Box)
		if err != nil || b.State != contract.BoxPending || b.Reason != string(box.PendingDisk) {
			t.Fatalf("%s: %+v, %v", id, b, err)
		}
	}
	if h.starts != 0 {
		t.Fatal("a box started below the floor")
	}
	lowDisk(t, s, ctx, 500)
	passLifecycle(t, s, ctx)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatalf("wake after disk freed: %d %s", h.starts, stateOf(t, s, ctx, sid))
	}
}

func TestEvictDiskArchivesBeforeRemoving(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
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
	lowDisk(t, s, ctx, 99)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxClosing {
		t.Fatalf("eviction must start the box to archive: %d %s", h.starts, stateOf(t, s, ctx, sid))
	}
	if _, problem := s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.ArchiveDoneRequest{}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{"session.closed"})
	if err != nil || len(events) != 1 || events[0].Body.(contract.SessionClosed).Reason != contract.SessionCloseDisk {
		t.Fatalf("closed events %+v: %v", events, err)
	}
}
