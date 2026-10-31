package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type retryTimeoutBoxes struct {
	service *Service
	calls   int
	states  []contract.BoxState
}

func (*retryTimeoutBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, errors.New("unavailable")
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

type blockingTimeoutBoxes struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingTimeoutBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (b *blockingTimeoutBoxes) Remove(context.Context, contract.BoxID) error {
	b.once.Do(func() { close(b.started); <-b.release })
	return nil
}

func TestHelloTimeoutRetriesCleanupBeforeFailure(t *testing.T) {
	s, room, session, ctx := setup(t)
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: tree.Root()}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
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
	t.Run("close wins", func(t *testing.T) {
		s, room, session, ctx := setup(t)
		tree, err := s.store.RoomTree(ctx, room)
		if err != nil {
			t.Fatal(err)
		}
		if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: tree.Root()}, contract.BoxHelloRequest{}); e != nil {
			t.Fatal(e)
		}
		b := &blockingTimeoutBoxes{started: make(chan struct{}), release: make(chan struct{})}
		s.boxes = b
		s.now = func() time.Time { return time.Unix(1061, 0) }
		swept := make(chan error, 1)
		go func() { swept <- s.FailStaleStarts(ctx) }()
		<-b.started
		closed := make(chan *contract.Error, 1)
		go func() {
			_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session})
			closed <- e
		}()
		close(b.release)
		if err := <-swept; err != nil {
			t.Fatal(err)
		}
		if e := <-closed; e != nil {
			t.Fatal(e)
		}
	})
}
