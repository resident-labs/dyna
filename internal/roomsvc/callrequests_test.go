package roomsvc

import (
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/listchange"
	"dyna-agent/internal/state"
	"encoding/json"
	"testing"
)

func TestCallRequestViewsDeliveryAndEvents(t *testing.T) {
	e := newEnv(t)
	rec := e.room("calls")
	db, err := database.Open(e.svc.Notify.CallRequestStore().Root)
	if err != nil {
		t.Fatal(err)
	}
	var published []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) { published = append(published, ev) })
	row, _, err := e.svc.Notify.CallRequestStore().Ask(rec.ID, "CI needs a sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 {
		t.Fatalf("published %d events, want one", len(published))
	}
	event := published[0].Body.(*listchange.Event)
	if event.Kind != "call-requests" || event.RoomID != rec.ID {
		t.Fatalf("event: %+v", event)
	}
	var wire callrequests.Request
	if err := json.Unmarshal(event.Change.Item, &wire); err != nil || wire.ID != row.ID || wire.Reason != row.Reason {
		t.Fatalf("row: %+v %v", wire, err)
	}
	rows, err := e.svc.CallRequests("")
	if err != nil || len(rows[rec.ID]) != 1 {
		t.Fatalf("open view: %+v %v", rows, err)
	}
	shown, err := e.svc.CallRequestShown(rec.ID, row.ID)
	if err != nil || shown.ShownAt == nil {
		t.Fatalf("shown: %+v %v", shown, err)
	}
	closed, err := e.svc.DeclineCallRequest(rec.ID, row.ID)
	if err != nil || closed.State != "declined" || closed.ClosedBy != "owner" {
		t.Fatalf("declined: %+v %v", closed, err)
	}
	rows, err = e.svc.CallRequests("")
	if err != nil || len(rows[rec.ID]) != 0 {
		t.Fatalf("closed in open view: %+v %v", rows, err)
	}
	rows, err = e.svc.CallRequests(rec.ID)
	if err != nil || len(rows[rec.ID]) != 1 || rows[rec.ID][0].State != "declined" {
		t.Fatalf("history: %+v %v", rows, err)
	}
	if _, err := e.svc.CallRequestShown(rec.ID, "c404"); err == nil {
		t.Fatal("unknown request accepted")
	}
}
