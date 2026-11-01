package engine

import (
	"context"
	"database/sql"
	"errors"
	"slices"
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

// holdLow keeps a host at free GiB for as long as eviction asks.
func holdLow(t *testing.T, s *Service, ctx context.Context, free int64) {
	t.Helper()
	lowDisk(t, s, ctx, free)
	later := s.now().Add(diskLowWait)
	s.now = func() time.Time { return later }
	lowDisk(t, s, ctx, free)
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
	if slices.Contains(h.removed, id) {
		t.Fatal("eviction removed a box before its archive")
	}
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
	if !slices.Contains(h.removed, id) {
		t.Fatal("box stayed after its archive")
	}
}

func TestOneLowReadingEvictsNothing(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	// A sample that dips and recovers, as a btrfs statfs or metadata reading can, must not close a session.
	lowDisk(t, s, ctx, 0)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	later := s.now().Add(diskLowWait)
	s.now = func() time.Time { return later }
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("a single sample closed a session")
	}
	// Reading the old sample after a restart is no better than reading it twice.
	if err := s.store.ResetEngineHosts(ctx); err != nil {
		t.Fatal(err)
	}
	lowDisk(t, s, ctx, 0)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("a sample from before the restart counted")
	}
}

func TestFailedBoxEvictsThroughArchive(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	id := boxOf(t, s, ctx, sid)
	passLifecycle(t, s, ctx)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='failed',want='run',reason='crashed' WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	holdLow(t, s, ctx, 99)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	if slices.Contains(h.removed, id) || h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxClosing {
		t.Fatalf("failed box must start to archive: removed %v starts %d state %s", h.removed, h.starts, stateOf(t, s, ctx, sid))
	}
	if _, problem := s.ArchiveDone(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.ArchiveDoneRequest{}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	events, err := s.store.Events(ctx, room, 0, 1000, []contract.EventType{"session.closed"})
	if err != nil || !slices.Contains(h.removed, id) || len(events) != 1 {
		t.Fatalf("removed %v closed %+v: %v", h.removed, events, err)
	}
}

// closingReason reads the session's box, which must still be closing with its volume kept.
func closingReason(t *testing.T, s *Service, ctx context.Context, h *dormantHost, sid contract.SessionID) string {
	t.Helper()
	record, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatalf("session closed without an archive: %v", err)
	}
	b, err := s.store.Box(ctx, record.Box)
	if err != nil || b.State != contract.BoxClosing || slices.Contains(h.removed, record.Box) {
		t.Fatalf("box %+v removed %v: %v", b, h.removed, err)
	}
	return b.Reason
}

func TestEvictedBoxWaitsWhenItCannotStart(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	h.startErr = errors.New("no space left on device")
	holdLow(t, s, ctx, 0)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	passLifecycle(t, s, ctx)
	if closingReason(t, s, ctx, h, sid) != "no space left on device" || h.starts != 1 {
		t.Fatalf("first try: starts %d", h.starts)
	}
	advance := func(by time.Duration) {
		later := s.now().Add(by)
		s.now = func() time.Time { return later }
		passLifecycle(t, s, ctx)
	}
	advance(31 * time.Second)
	if closingReason(t, s, ctx, h, sid) == "" || h.starts != 2 {
		t.Fatalf("second try: starts %d", h.starts)
	}
	// A box that keeps failing waits the archive retry, so stuck closes cannot fill the host's step slots.
	advance(31 * time.Second)
	if h.starts != 2 {
		t.Fatalf("retried at the short delay: starts %d", h.starts)
	}
	advance(10 * time.Minute)
	if closingReason(t, s, ctx, h, sid) == "" || h.starts != 3 {
		t.Fatalf("timed retry: starts %d", h.starts)
	}
}

func TestCloseShowsBoxThatNeverConnects(t *testing.T) {
	s, h, _, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if closingReason(t, s, ctx, h, sid) != "" || h.starts != 1 {
		t.Fatalf("a box just started is not stuck: starts %d", h.starts)
	}
	later := s.now().Add(31 * time.Second)
	s.now = func() time.Time { return later }
	passLifecycle(t, s, ctx)
	if got := closingReason(t, s, ctx, h, sid); got == "" {
		t.Fatal("a box that never connected looks like a running close")
	}
	// It may only have been slow: once connected it archives at once.
	channel, problem := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if got := closingReason(t, s, ctx, h, sid); got != "" {
		t.Fatalf("still stuck after connecting: %s", got)
	}
	for message := range channel {
		if _, ok := message.Body.(contract.Archive); ok {
			return
		}
	}
	t.Fatal("no archive after connecting")
}
