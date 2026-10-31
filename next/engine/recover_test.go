package engine

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type recoveryBoxes struct{ removed []contract.BoxID }

func (*recoveryBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (b *recoveryBoxes) Remove(_ context.Context, id contract.BoxID) error {
	b.removed = append(b.removed, id)
	return nil
}

func TestRecoverInterruptedCreation(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   contract.BoxState
		want    contract.BoxState
		removed bool
	}{
		{"creating is retried", contract.BoxCreating, contract.BoxStarting, true},
		{"starting waits for hello", contract.BoxStarting, contract.BoxStarting, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, session, ctx := setup(t)
			record, err := s.store.Session(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE boxes SET state = ?, due_at = 0 WHERE id = ?", test.state, record.Box)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			fake := &recoveryBoxes{}
			s.boxes = fake
			resetLifecycle(s)
			s.reconciler = lifecycle.New(s.store, s.boxes, s, s.repos, s.event, func() time.Time { return s.now() })
			if err := s.reconciler.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			s.reconciler.Wait()
			current, err := s.store.Box(ctx, record.Box)
			if err != nil {
				t.Fatal(err)
			}
			if current.State != test.want || (len(fake.removed) > 0) != test.removed {
				t.Fatalf("state %s, removed %v", current.State, fake.removed)
			}

		})
	}
}
