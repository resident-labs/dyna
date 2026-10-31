package main

import (
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

// roomBundleCounts counts decoded records independently of declared counts.
func roomBundleCounts(b contract.RoomBundle) contract.BundleCounts {
	counts := contract.BundleCounts{
		Rules: len(b.Rules), Goals: len(b.Goals), Issues: len(b.Issues),
		PRs: len(b.PRs), Questions: len(b.Questions),
		Subscriptions: len(b.Subscriptions), Sessions: len(b.Handoff),
	}
	for _, goal := range b.Goals {
		counts.GoalItems += len(goal.Items)
	}
	return counts
}

func globalBundleCounts(b contract.GlobalBundle) contract.BundleCounts {
	return contract.BundleCounts{
		Rules: len(b.Rules), Hosts: len(b.Hosts), Accounts: len(b.Accounts),
		Devices: len(b.Devices), Subscriptions: len(b.Subscriptions),
	}
}

func verifyRoomCounts(b contract.RoomBundle, source roomSQLCounts) error {
	got := roomBundleCounts(b)
	if got != b.Counts {
		return fmt.Errorf("room declared counts differ from decoded records")
	}
	if got.Rules != source.Rules || got.Goals != source.Goals || got.GoalItems != source.GoalItems ||
		got.Issues != source.Issues || got.Questions != source.Questions ||
		got.PRs != source.PRs || got.Sessions != source.Sessions || got.Subscriptions != source.Subscriptions {
		return fmt.Errorf("room SQL counts differ from decoded records")
	}
	return nil
}
