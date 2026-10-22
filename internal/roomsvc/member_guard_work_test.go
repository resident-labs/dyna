package roomsvc

import (
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestCoveredMemberWriteDoesNotRepeatTheFullScan(t *testing.T) {
	e, rec, sessions, w := seedTargetedMembers(t)
	records := []room.Record{rec}
	if !e.svc.membersChanged(w, records, true) {
		t.Fatal("the initial scan was skipped")
	}
	if _, err := e.mgr.Store.UpdateMeta(sessions[0].ID, func(meta *subagent.Meta) { meta.Profile = "covered" }); err != nil {
		t.Fatal(err)
	}
	var scanned []int
	e.svc.onMemberScan = func(n int) { scanned = append(scanned, n) }
	e.svc.SessionChanged(sessions[0].ID)
	var events []Event
	e.svc.scanDirtyMembers(w, func(event Event) { events = append(events, event) })
	if len(scanned) != 1 || scanned[0] != 1 || len(events) != 2 || w.view == nil {
		t.Fatalf("targeted work = %v, events = %+v, shared = %v; want one covered member", scanned, events, w.view != nil)
	}
	for range MemberFullScanEvery + 1 {
		if e.svc.membersChanged(w, records, false) {
			t.Fatal("a tick repeated member work already covered by the targeted scan")
		}
	}
	// A later write without a wake must still pass the revision guard.
	if _, err := e.mgr.Store.UpdateMeta(sessions[1].ID, func(meta *subagent.Meta) { meta.Profile = "unwoken" }); err != nil {
		t.Fatal(err)
	}
	if !e.svc.membersChanged(w, records, false) {
		t.Fatal("a later unwoken write did not reach the fallback scan")
	}
}
