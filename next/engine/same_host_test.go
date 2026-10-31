package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestSameHostPlacement(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if err := s.store.AddHost(ctx, box.Host{Name: "other", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE hosts SET cpus=0 WHERE name='host'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	child, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "pinned", Profile: "test", SameHost: true})
	if problem != nil {
		t.Fatal(problem)
	}
	before, err := s.store.Box(ctx, child.Box)
	if err != nil || before.Host != "host" {
		t.Fatalf("pin not stored: %+v, %v", before, err)
	}
	passLifecycle(t, s, ctx)
	after, err := s.store.Box(ctx, child.Box)
	if err != nil || after.Host != "host" || after.State != contract.BoxPending || after.Reason != "cpu" {
		t.Fatalf("pinned box moved or started: %+v, %v", after, err)
	}
	unplaced, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "unplaced", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: unplaced.Session, Name: "invalid", Profile: "test", SameHost: true})
	if problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("unplaced parent: %v", problem)
	}
}
