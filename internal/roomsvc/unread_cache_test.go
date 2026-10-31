package roomsvc

import "testing"

func TestRoomsReusesUnreadScanUntilLogOrReadsChange(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	first := e.say(rec, "lead", "first")
	scans := 0
	e.svc.onUnreadScan = func(roomID string) {
		if roomID == rec.ID {
			scans++
		}
	}
	check := func(wantUnread, wantScans int) {
		t.Helper()
		if got := roomUnreadCount(t, e.svc, rec.ID); got != wantUnread {
			t.Fatalf("unread = %d, want %d", got, wantUnread)
		}
		if scans != wantScans {
			t.Fatalf("unread scans = %d, want %d", scans, wantScans)
		}
	}
	check(1, 1)
	check(1, 1)
	e.say(rec, "lead", "second")
	check(2, 2)
	check(2, 2)
	if err := e.svc.MarkRead(rec.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	check(1, 3)
	check(1, 3)
}

// Read posts must not cause repeated home-index lookups on every room list.
func TestRoomUnreadChecksHomesOnlyForUnreadPosts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	first := e.say(rec, "lead", "first")
	e.say(rec, "lead", "second")
	if err := e.svc.MarkRead(rec.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	checked := -1
	e.svc.onOwnerHomes = func(n int) { checked = n }
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 1 {
		t.Fatalf("unread = %d; want 1", got)
	}
	if checked != 1 {
		t.Fatalf("checked %d post homes; want only the unread post", checked)
	}
	if err := e.svc.MarkRoomRead(rec.ID); err != nil {
		t.Fatal(err)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 0 {
		t.Fatalf("unread after marking room read = %d", got)
	}
	if checked != 0 {
		t.Fatalf("checked %d post homes after marking read; want 0", checked)
	}
}
