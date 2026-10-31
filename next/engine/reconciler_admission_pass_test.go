package engine

import (
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

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
