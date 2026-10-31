package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type movingHost struct {
	fakeBoxes
	mu        sync.Mutex
	stopped   []contract.BoxID
	oldSlices []string
	recreated []BoxSpec
	failStop  bool
}

func (h *movingHost) Stop(_ context.Context, id contract.BoxID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, id)
	if h.failStop {
		return errors.New("stop failed")
	}
	return nil
}
func (h *movingHost) Recreate(_ context.Context, spec BoxSpec, _, oldSlice string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.oldSlices = append(h.oldSlices, oldSlice)
	h.recreated = append(h.recreated, spec)
	return nil
}

func TestMoveLeavesRunningBoxesUnchanged(t *testing.T) {
	s, room, root, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: root, Name: "child", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	destination, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "destination", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	before, err := s.store.Box(ctx, child.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, e = s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: destination.Session}); e != nil {
		t.Fatal(e)
	}
	after, err := s.store.Box(ctx, child.Box)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("box changed: before %+v after %+v", before, after)
	}
}
