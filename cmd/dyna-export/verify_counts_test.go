package main

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestVerifyRoomCountsRejectsInventedSubscription(t *testing.T) {
	b := contract.RoomBundle{Subscriptions: []contract.Subscription{{}}}
	b.Counts = roomBundleCounts(b)
	if err := verifyRoomCounts(b, roomSQLCounts{}); err == nil {
		t.Fatal("accepted a subscription with no eligible v1 hook")
	}
}

func TestVerifyRoomCountsRejectsMissingGoalItem(t *testing.T) {
	b := contract.RoomBundle{Goals: []contract.GoalBundle{{Items: []contract.GoalItemBundle{{}}}}}
	b.Counts = roomBundleCounts(b)
	if err := verifyRoomCounts(b, roomSQLCounts{Goals: 1, GoalItems: 2}); err == nil {
		t.Fatal("accepted missing goal item")
	}
}
