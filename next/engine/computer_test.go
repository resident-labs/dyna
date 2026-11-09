package engine

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// failedStart is a box whose desktop does not start, and says why the way the box's dyna does.
type failedStart struct{ fakeBoxes }

func (failedStart) ExecArgv(context.Context, contract.HostName, contract.BoxID, ...string) ([]string, error) {
	return []string{"sh", "-c", "echo 'Error: computer start: 500 Internal Server Error' >&2; exit 1"}, nil
}

func TestComputerStartIsTheOwnersAndNeedsAReadyBox(t *testing.T) {
	s, _, id, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	req := contract.ComputerStartRequest{Session: id}
	if _, err := s.ComputerStart(ctx, owner, req); err == nil || err.Code != contract.ErrConflict {
		t.Fatalf("box not ready: %v", err)
	}
	connectAgent(ctx, t, s, id)
	if _, err := s.ComputerStart(ctx, owner, req); err != nil {
		t.Fatal(err)
	}
	s.boxes = failedStart{}
	if _, err := s.ComputerStart(ctx, owner, req); err == nil || err.Code != contract.ErrUnavailable || err.Message != "the desktop did not start: computer start: 500 Internal Server Error" {
		t.Fatalf("failed start: %v", err)
	}
}

// The view's stream runs the box's dyna computer stream, and keeps the box awake until it closes.
func TestComputerStreamHoldsTheBoxAwake(t *testing.T) {
	s, _, id, ctx := setup(t)
	connectAgent(ctx, t, s, id)
	argv, release, problem := s.ComputerStream(ctx, id)
	if problem != nil {
		t.Fatal(problem)
	}
	record, err := s.store.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil || strings.TrimSpace(string(out)) != string(record.Box)+" dyna computer stream" {
		t.Fatalf("stream command %q: %v", out, err)
	}
	if !s.OpenTerminal(id) {
		t.Fatal("an open view must keep the box awake")
	}
	release()
	if s.OpenTerminal(id) {
		t.Fatal("a closed view still holds the box")
	}
}
