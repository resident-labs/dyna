package roomsvc

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func processReadBytes(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "rchar:") {
			value, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "rchar:")), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatal("process read-byte counter missing")
	return 0
}

func TestWarmRoomsReadsOnlyNewLogBytes(t *testing.T) {
	e := newEnv(t)
	rec := largeOwnerLog(t, e)
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 120 {
		t.Fatalf("initial large unread count is %d; want 120", got)
	}
	const budget = 64 << 10
	before := processReadBytes(t)
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 120 {
		t.Fatalf("warm unread count: %d", got)
	}
	if read := processReadBytes(t) - before; read > budget {
		t.Fatalf("unchanged Rooms read %d bytes; want at most %d", read, budget)
	}
	e.say(rec, "lead", "Only this append should be read")
	before = processReadBytes(t)
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 121 {
		t.Fatalf("append unread count: %d", got)
	}
	if read := processReadBytes(t) - before; read > budget {
		t.Fatalf("appended Rooms read %d bytes; want at most %d", read, budget)
	}
}

func TestFeedViewUnreadReusesWarmRoomLogIndex(t *testing.T) {
	e := newEnv(t)
	rec := largeOwnerLog(t, e)
	lead := e.start(rec, "lead", nil)
	e.start(rec, "child", &lead)
	e.say(rec, "child", "feed view unread")
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 121 {
		t.Fatalf("room unread = %d", got)
	}
	before := processReadBytes(t)
	feed, err := e.svc.FeedTail(rec.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if feed.ViewUnread["channel:"+lead.ID] != 1 || feed.Unread != 121 {
		t.Fatalf("feed unread: %+v", feed)
	}
	// The large log was warmed by Rooms. View counts must share that index,
	// rather than reading the retained log again for a separate unread system.
	if read := processReadBytes(t) - before; read > 64<<10 {
		t.Fatalf("view unread read %d bytes, want at most 64 KiB", read)
	}
}
