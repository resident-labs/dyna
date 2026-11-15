package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestAdmissionWakeHoldersExcludeRunners(t *testing.T) {
	for _, reason := range []string{"cpu", "memory"} {
		t.Run(reason, func(t *testing.T) {
			r := newGoalRig(t)
			r.exec("UPDATE boxes SET host='host',image='existing' WHERE session=?", r.child)
			r.exec("UPDATE boxes SET host='host',image='runner' WHERE session=?", r.lead)
			r.exec("UPDATE boxes SET state='pending',reason=? WHERE session=?", reason, r.child)
			r.advance(5 * time.Minute)
			if err := r.s.AdmissionNotices(r.ctx); err != nil {
				t.Fatal(err)
			}
			var text string
			if err := r.st.Read().QueryRowContext(r.ctx, "SELECT json_extract(body,'$.text') FROM events WHERE type='session.notice' LIMIT 1").Scan(&text); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "/lead:") {
				t.Fatalf("runner is not a wake blocker: %s", text)
			}
		})
	}
}

func TestAdmissionRoomLimitNumbersAndHolders(t *testing.T) {
	r := newGoalRig(t)
	r.exec("UPDATE boxes SET host='host' WHERE session=?", r.lead)
	r.exec("UPDATE boxes SET state='pending',reason='room-limit' WHERE session=?", r.child)
	r.exec("UPDATE rooms SET cpus=1,mem_bytes=? WHERE id=?", int64(1<<30), r.room)
	other, p := r.s.RoomCreate(r.ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if p != nil {
		t.Fatal(p)
	}
	r.exec("UPDATE boxes SET state='ready',host='host' WHERE session IN (SELECT id FROM sessions WHERE room=?)", other.Room)
	r.advance(5 * time.Minute)
	if err := r.s.AdmissionNotices(r.ctx); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT json_extract(body,'$.text') FROM events WHERE type='session.notice' AND room=? LIMIT 1", r.room).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "of 1 cores reserved") || !strings.Contains(text, "Moving hosts does not release a room limit") || !strings.Contains(text, "/lead:") || strings.Contains(text, string(other.Room)+":") {
		t.Fatal(text)
	}
}
