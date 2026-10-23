package watch

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/room"
)

func TestTransferStoredUndeliveredNoticeUsesNewPostID(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	rs := &room.Store{Root: t.TempDir()}
	rec, err := rs.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	e.s.Deliver = func(_ context.Context, r Record, n Notice) (string, error) {
		msg, err := rs.PostEventInput(room.PostEventInput{RoomID: rec.ID, ID: n.ID, Event: prEventOf(r, n), To: []string{n.Target}, RoomService: true})
		if err != nil {
			return "", err
		}
		if n.Target == e.author.ID {
			return msg.ID, errors.New("stored but delivery failed")
		}
		return msg.ID, nil
	}
	if err := e.s.Store.update(func(st *State) error {
		notice(st.Records[r.Key], e.author.ID, "CI failure", "stored-before-transfer")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.flush(context.Background(), r.Key); err == nil {
		t.Fatal("expected initial delivery failure")
	}
	before, _ := rs.Messages(rec.ID)
	if len(before) != 1 {
		t.Fatalf("initial stored post: %+v", before)
	}
	originalID := before[0].ID
	if err := e.s.Transfer(context.Background(), r.Key, "author", transferTarget(e), ""); err != nil {
		t.Fatalf("successor did not receive stored notice: %v", err)
	}
	after, err := rs.Messages(rec.ID)
	if err != nil || len(after) != 3 {
		t.Fatalf("successor notices: %+v %v", after, err)
	}
	if after[0].ID != originalID || after[0].To[0] != e.author.ID {
		t.Fatal("transfer changed the original stored post")
	}
	if after[1].ID == originalID || after[1].To[0] != transferTarget(e).ID {
		t.Fatalf("successor reused the old post: %+v", after[1])
	}
	record := e.record()
	if record.Notices[0].SupersededBy != record.Notices[1].ID || record.Notices[0].Error == "" {
		t.Fatalf("original notice history lost: %+v", record.Notices)
	}
	if err := e.s.Transfer(context.Background(), r.Key, "author", transferTarget(e), ""); err != nil {
		t.Fatal(err)
	}
	retry, _ := rs.Messages(rec.ID)
	if len(retry) != 3 {
		t.Fatal("retry duplicated a transfer notice")
	}
}
