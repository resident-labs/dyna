package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

var statsOwner = contract.Principal{Kind: contract.PrincipalOwner}

func TestBoxStatsIsTheOwnersAndItsReportTheBoxes(t *testing.T) {
	s, _, id, ctx := setup(t)
	agent := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	if _, problem := s.BoxStats(ctx, agent, contract.BoxStatsRequest{Session: id}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session read box stats: %v", problem)
	}
	if _, problem := s.BoxStatsReport(ctx, statsOwner, contract.BoxStatsReportRequest{}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("the owner reported box stats: %v", problem)
	}
}
