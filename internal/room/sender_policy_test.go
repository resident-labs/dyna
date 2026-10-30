package room

import (
	"testing"
	"time"
)

func TestCrossRoomSenderPolicy(t *testing.T) {
	s := recordsStore(t)
	now := time.Unix(1700000000, 0)
	s.Now = func() time.Time { return now }
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(from, senderRoom, id string, want DeliveryMode) {
		t.Helper()
		msg, err := s.Post(rec.ID, PostInput{Wake: true, From: from, SenderRoomID: senderRoom, SessionIDs: map[string]string{from: id, "work:agent": "ses_agent"}, To: []string{"work:root", "work:agent"}, Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		row, ok, err := s.SeatDelivery(rec.ID, msg.ID)
		if err != nil || !ok || row.Mode != want {
			t.Fatalf("seat = %+v, %v, %v; want %s", row, ok, err, want)
		}
		if want == DeliveryQuiet && row.Category != RootQuietPost {
			t.Fatalf("quiet category = %q", row.Category)
		}
		rows, err := s.SessionDeliveries(rec.ID, []string{"ses_agent"}, []string{msg.ID})
		if err != nil || rows[msg.ID].Mode != DeliveryWake {
			t.Fatalf("other recipient = %+v, %v", rows, err)
		}
		stored, err := s.Message(rec.ID, msg.ID)
		if err != nil || stored.Text != "hello" {
			t.Fatalf("stored post = %+v, %v", stored, err)
		}
	}
	if err := s.SetSenderMute(rec.ID, "session", "ses_one", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	post("other:one", "room_other", "ses_one", DeliveryQuiet)
	post("other:two", "room_other", "ses_two", DeliveryWake)
	now = now.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		post("other:one", "room_other", "ses_one", DeliveryWake)
	}
	post("other:one", "room_other", "ses_one", DeliveryQuiet)
	post("other:root", "room_other", "ses_root", DeliveryWake)
	now = now.Add(2 * time.Minute)
	post("other:one", "room_other", "ses_one", DeliveryWake)
	if err := s.SetSenderMute(rec.ID, "room", "room_other", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	post("other:two", "room_other", "ses_two", DeliveryQuiet)
	post("other:root", "room_other", "ses_root", DeliveryWake)
	if err := s.SetSenderMute(rec.ID, SenderMuteRoom, "room_other", time.Time{}); err != nil {
		t.Fatal(err)
	}
	post("other:two", "room_other", "ses_two", DeliveryWake)
}
