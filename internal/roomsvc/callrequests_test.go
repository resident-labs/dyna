package roomsvc

import (
	"context"
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
	// Each engine has its own watch state. Only the first receives the
	// writing process's publication hook; the second must detect SQL changes.
	consumers := []*Service{e.svc, New(e.svc.Notify)}
	signatures := []map[string]string{{}, {}}
	versions := []int64{0, 0}
	received := make([][]Event, 2)
	for i, consumer := range consumers {
		consumer.scanCallRequests(signatures[i], &versions[i], true, func(Event) { t.Fatal("initial event") })
	}
	var published []hub.Event
	state.For(db).SetPublish(func(ev hub.Event) {
		published = append(published, ev)
		consumers[0].CallRequestsChanged()
	})
	checkConsumers := func(want int, state string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		unexpected := func() { t.Fatal("call-request wake scanned another store") }
		if consumers[0].waitTick(ctx, nil, unexpected, unexpected, unexpected, func() {
			consumers[0].scanCallRequests(signatures[0], &versions[0], false, func(ev Event) { received[0] = append(received[0], ev) })
			cancel()
		}) {
			t.Fatal("local call-request change required a tick")
		}
		select {
		case <-consumers[1].callRequestWake:
			t.Fatal("second process received a local wake")
		default:
		}
		consumers[1].scanCallRequests(signatures[1], &versions[1], false, func(ev Event) { received[1] = append(received[1], ev) })
		for i, consumer := range consumers {
			// A later tick must not duplicate the immediate event or the fallback.
			consumer.scanCallRequests(signatures[i], &versions[i], false, func(ev Event) { received[i] = append(received[i], ev) })
			if len(received[i]) != want {
				t.Fatalf("consumer %d has %d events, want %d", i, len(received[i]), want)
			}
			event := received[i][want-1]
			var row callrequests.Request
			if err := json.Unmarshal(event.Change.Item, &row); err != nil || event.Kind != callrequests.Topic || event.RoomID != rec.ID || row.State != state {
				t.Fatalf("consumer %d event: %+v, row %+v, error %v", i, event, row, err)
			}
		}
	}
	row, _, err := e.svc.Notify.CallRequestStore().Ask(rec.ID, "CI needs a sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM call_requests_revision").Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("committed call-request revision = %d, %v; want 1", revision, err)
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
	checkConsumers(1, "ringing")
	rows, err := e.svc.CallRequests("")
	if err != nil || len(rows[rec.ID]) != 1 {
		t.Fatalf("open view: %+v %v", rows, err)
	}
	shown, err := e.svc.CallRequestShown(rec.ID, row.ID)
	if err != nil || shown.ShownAt == nil {
		t.Fatalf("shown: %+v %v", shown, err)
	}
	checkConsumers(2, "ringing")
	closed, err := e.svc.DeclineCallRequest(rec.ID, row.ID)
	if err != nil || closed.State != "declined" || closed.ClosedBy != "owner" {
		t.Fatalf("declined: %+v %v", closed, err)
	}
	checkConsumers(3, "declined")
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
