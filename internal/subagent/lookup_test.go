package subagent

import (
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// A consolidation target answers to the names its consumed sessions held, so
// the claim checks every mention on the way to a report decision. That lookup
// must not read the stored records of every other session while the gate is
// held.
func TestConsolidationClaimLooksUpNamesWithoutListingRecords(t *testing.T) {
	m, _, work, metas := actorFixture(t)
	child := metas["child"]
	consumed := Meta{
		ID: "ses_20261018-000005-eeeeeeee", Name: "old", RoomID: work.ID, Room: work.Name,
		Profile: "mock", ConsumedBy: child.ID, Closed: time.Date(2026, 10, 18, 0, 5, 0, 0, time.UTC),
	}
	if err := m.Store.writeMeta(consumed); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.UpdateMeta(child.ID, func(meta *Meta) {
		meta.InheritedInboxes = []InheritedInbox{{Session: consumed.ID, Address: consumed.Address()}}
	}); err != nil {
		t.Fatal(err)
	}
	child, err := m.Store.ReadMeta(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var via []string
	for i := range 30 {
		via = append(via, fmt.Sprintf("@agent-%d", i))
	}
	via = append(via, "@old")
	reads := 0
	beforeRoomMetaRead = func(string) { reads++ }
	t.Cleanup(func() { beforeRoomMetaRead = func(string) {} })
	if !m.Store.Owes(child, room.Message{From: work.Name, Via: via}) {
		t.Error("a post that names a consumed session does not owe its consolidation target a report")
	}
	if reads != 0 {
		t.Errorf("the claim read %d session records, want 0", reads)
	}
}
