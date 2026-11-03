package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// host.list names sessions only in rooms the caller can read, by the same rule as session.list.
func TestHostListSessionsFollowRoomAccess(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	rooms := func(who contract.Principal) map[contract.RoomID]bool {
		t.Helper()
		list, problem := s.HostList(ctx, who, contract.HostListRequest{})
		if problem != nil || len(list.Hosts) != 2 {
			t.Fatalf("hosts: %+v %v", list, problem)
		}
		got := make(map[contract.RoomID]bool)
		for _, session := range list.Hosts[1].Sessions {
			got[session.Room] = true
		}
		return got
	}
	if got := rooms(owner); !got[room] || !got[other.Room] {
		t.Fatalf("owner: %+v", got)
	}
	if got := rooms(contract.Principal{Kind: contract.PrincipalSession, Session: worker}); !got[room] || got[other.Room] {
		t.Fatalf("session: %+v", got)
	}
	if got := rooms(contract.Principal{Kind: contract.PrincipalEngine}); len(got) != 0 {
		t.Fatalf("engine: %+v", got)
	}
}

// host list's low flag uses the engine's warn level from the stored disk size: a pool node at its floor, others at floor plus headroom.
func TestHostListDiskLowMatchesEngineWarnLevel(t *testing.T) {
	s, _, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, tc := range []struct {
		pool string
		free int64
		low  bool
	}{
		{"", 70, false}, // 146 GiB disk warns at 56.5 GiB, not 100
		{"", 56, true},
		{"boats", 50, false},
		{"boats", 21, false},
		{"boats", 19, true},
	} {
		if err := s.store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE hosts SET pool=?, disk_bytes=?, disk_floor=?, disk_total=?, checked_at=1 WHERE name='host'", tc.pool, tc.free*box.GiB, 20*box.GiB, 146*box.GiB)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		list, problem := s.HostList(ctx, owner, contract.HostListRequest{})
		if problem != nil || list.Hosts[1].DiskLow != tc.low {
			t.Fatalf("%+v: %+v %v", tc, list.Hosts, problem)
		}
	}
}

// An exec-only host has no disk figures, so host list never calls it low on disk.
func TestHostListExecOnlyIsNeverDiskLow(t *testing.T) {
	s, _, _, ctx := setup(t)
	if err := s.store.AddHost(ctx, box.Host{Name: "omarchy", State: box.HostReady, NoAgents: true, ExecOnly: true, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	list, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, h := range list.Hosts {
		if h.Name == "omarchy" && (!h.ExecOnly || h.DiskLow) {
			t.Fatalf("%+v", h)
		}
	}
}
