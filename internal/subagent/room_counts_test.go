package subagent

import "testing"

func TestRoomMemberCountsTracksDoneAndDismissal(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: finished")
	info, _ = e.wait(info.ID, 1)
	if _, err := e.finish(info.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		counts, err := e.m.Store.RoomMemberCounts()
		if err != nil || counts[info.RoomID].Members != 1 || counts[info.RoomID].Running != 0 {
			t.Fatalf("done member count: %+v, %v", counts, err)
		}
	}
	ownerDismissEnv(t)
	if _, err := e.m.Close(e.ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	counts, err := e.m.Store.RoomMemberCounts()
	if err != nil || counts[info.RoomID].Members != 0 {
		t.Fatalf("dismissed member count: %+v, %v", counts, err)
	}
}
