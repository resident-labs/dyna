package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type retryTimeoutBoxes struct {
	service *Service
	calls   int
	states  []contract.BoxState
}

func (*retryTimeoutBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (b *retryTimeoutBoxes) Remove(ctx context.Context, id contract.BoxID) error {
	current, err := b.service.store.Box(ctx, id)
	if err != nil {
		return err
	}
	b.states = append(b.states, current.State)
	b.calls++
	if b.calls == 1 {
		return errors.New("host unavailable")
	}
	return nil
}

func TestHelloTimeoutRetriesCleanupBeforeFailure(t *testing.T) {
	s, _, session, ctx := setup(t)
	b := &retryTimeoutBoxes{service: s}
	s.boxes = b
	s.now = func() time.Time { return time.Unix(1061, 0) }
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []contract.BoxState{contract.BoxStarting, contract.BoxFailed} {
		if err := s.FailStaleStarts(ctx); err != nil {
			t.Fatal(err)
		}
		got, err := s.store.Box(ctx, record.Box)
		if err != nil || got.State != want || b.calls != i+1 || b.states[i] != contract.BoxStarting {
			t.Fatalf("attempt %d: box=%+v calls=%d states=%v err=%v", i, got, b.calls, b.states, err)
		}
	}
}
