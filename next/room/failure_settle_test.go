package room

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// A box back from a told failure keeps its record until it has stayed up for FailureSettle, so going again in that time
// is not news.
func TestPlanFailuresWaitsForABoxToSettle(t *testing.T) {
	sessions := family()
	child := childOf(sessions)
	child.FailureTold, child.BoxChanged = FailureBox, checkinNow.Add(-FailureSettle+time.Second)
	if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || len(settled) != 0 {
		t.Fatalf("a box just back: plans %+v, settled %v", plans, settled)
	}
	child.Failure, child.Box = FailureBox, contract.BoxLost
	if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || len(settled) != 0 {
		t.Fatalf("the same box gone again: plans %+v, settled %v", plans, settled)
	}
	child.Failure, child.Box = FailureTurn, contract.BoxReady
	if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || len(settled) != 0 {
		t.Fatalf("the turn it took down: plans %+v, settled %v", plans, settled)
	}
	child.Failure, child.BoxChanged = "", checkinNow.Add(-FailureSettle)
	if plans, settled := PlanFailures(sessions, checkinNow); len(plans) != 0 || settled["child"] != "" || len(settled) != 1 {
		t.Fatalf("a box that stayed up: plans %+v, settled %v", plans, settled)
	}
}
