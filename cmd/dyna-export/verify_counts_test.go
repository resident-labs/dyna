package main

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestInstructionCountsUseRunesAndRejectMissingDocuments(t *testing.T) {
	global := contract.GlobalBundle{Instructions: "界"}
	room := contract.RoomBundle{Instructions: "é界"}
	if globalBundleCounts(global).InstructionChars != 1 || roomBundleCounts(room).InstructionChars != 2 {
		t.Fatal("instruction count uses bytes instead of runes")
	}
	room.Counts = roomBundleCounts(room)
	room.Instructions = ""
	if err := verifyRoomCounts(room, roomSQLCounts{}); err == nil {
		t.Fatal("accepted missing instruction text")
	}
}

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
