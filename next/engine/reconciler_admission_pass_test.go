package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestRoomLimitAcrossHostsInOnePass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cpus   int
		memory int64
	}{{"cpu", 2, 0}, {"memory", 0, 4 * box.GiB}} {
		t.Run(tc.name, func(t *testing.T) {
			s, room, _, ctx := setup(t)
			if err := s.store.AddHost(ctx, box.Host{Name: "other", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
				t.Fatal(err)
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE rooms SET cpus=?,mem_bytes=? WHERE id=?", tc.cpus, tc.memory, room)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var ids []contract.BoxID
			for _, name := range []string{"one", "two"} {
				result, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: name, Profile: "test", Size: contract.BoxSizeSmall})
				if problem != nil {
					t.Fatal(problem)
				}
				ids = append(ids, result.Box)
			}
			passLifecycle(t, s, ctx)
			placed, waiting := 0, 0
			for _, id := range ids {
				b, err := s.store.Box(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case b.State == contract.BoxStarting:
					placed++
				case b.State == contract.BoxPending && b.Reason == "room-limit":
					waiting++
				default:
					t.Fatalf("unexpected box: %+v", b)
				}
			}
			if placed != 1 || waiting != 1 {
				t.Fatalf("placed %d waiting %d", placed, waiting)
			}
		})
	}
}

func TestAdmissionPassCountsEarlierPlacements(t *testing.T) {
	s, room, _, ctx := setup(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=2")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	var ids []contract.BoxID
	for _, name := range []string{"one", "two"} {
		result, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{
			Room: room, Name: name, Profile: "test", Size: contract.BoxSizeSmall,
		})
		if problem != nil {
			t.Fatal(problem)
		}
		ids = append(ids, result.Box)
	}
	passLifecycle(t, s, ctx)
	placed, waiting := 0, 0
	for _, id := range ids {
		b, err := s.store.Box(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case b.State == contract.BoxStarting:
			placed++
		case b.State == contract.BoxPending && b.Reason == "cpu":
			waiting++
		default:
			t.Fatalf("unexpected box state: %+v", b)
		}
	}
	if placed != 1 || waiting != 1 {
		t.Fatalf("placed %d, waiting %d", placed, waiting)
	}
}
