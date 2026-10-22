package roomsvc

import (
	"dyna-agent/internal/changepolicy"
	"strings"
	"testing"
)

func TestPolicyOnlyChangeRefreshesRoom(t *testing.T) {
	e := newEnv(t)
	room := e.room("policy-events")
	signatures, version := map[string]string{}, ""
	work := map[string]string{}
	var events []Event
	emit := func(event Event) { events = append(events, event) }
	e.svc.scanPRs(signatures, work, &version, true, emit)
	if err := e.svc.PRs.SetRoomChangePolicy(ownerCall, room.ID, changepolicy.Default()); err != nil {
		t.Fatal(err)
	}
	e.svc.scanPRs(signatures, work, &version, false, emit)
	if len(events) != 1 || events[0].Kind != "prs" || !strings.EqualFold(events[0].RoomID, room.ID) {
		t.Fatalf("missing policy event %+v", events)
	}
}
