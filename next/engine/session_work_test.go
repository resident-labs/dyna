package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionWorkInvalidCursor(t *testing.T) {
	s, _, session, ctx := setup(t)
	for _, tt := range []struct {
		name   string
		before contract.TurnID
	}{
		{name: "unknown turn", before: "missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, problem := s.SessionWork(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionWorkRequest{Session: session, Before: tt.before})
			if problem == nil || problem.Code != contract.ErrInvalid {
				t.Fatalf("cursor error = %v, want invalid", problem)
			}
		})
	}
}
