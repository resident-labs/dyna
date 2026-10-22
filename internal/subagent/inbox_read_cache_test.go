package subagent

import (
	"fmt"
	"testing"

	"dyna-agent/internal/room"
)

// Every web request for room controls reads each session's pending
// messages. A session that inherited an inbox must not decode its whole
// inherited room history on each read: the live profile showed 16% of engine
// CPU in json.Unmarshal of inbox messages.
func TestInboxReadDecodesOnlyNewRecords(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("RESPOND: target SLEEP:1")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: source SLEEP:1")
	b, _ = e.wait(b.ID, 1)
	store, rooms := e.m.Store, e.m.rooms()
	_, cursor, err := rooms.ReadAll(a.RoomID, 0)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.UpdateMeta(a.ID, func(meta *Meta) {
		meta.InheritedInboxes = []InheritedInbox{{Session: b.ID, Address: b.Address(), Cursor: cursor}}
	})
	if err != nil {
		t.Fatal(err)
	}
	// History after the inherited cursor that is not for the source.
	chatter := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := rooms.Post(a.RoomID, room.PostInput{From: a.Room, To: []string{"@nobody"}, Text: fmt.Sprintf("chatter %d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func() []room.Message {
		t.Helper()
		got, err := store.inboxMessages(rooms, meta, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	decoded := func() int {
		r := readInboxFor(store.root()+"\x00"+a.ID, false)
		r.Lock()
		defer r.Unlock()
		return r.cache.decoded
	}
	chatter(50)
	read()
	before := decoded()
	chatter(450)
	for range 3 {
		read()
	}
	if got := decoded() - before; got > 450 {
		t.Fatalf("three inbox reads after 450 new posts decoded %d room records; old records must not be decoded again", got)
	}
	// A new post for the inherited address is still found.
	msg, err := rooms.Post(a.RoomID, room.PostInput{From: a.Room, To: []string{b.Address()}, Text: "for the source"})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 1 || got[0].ID != msg.ID {
		t.Fatalf("inbox after a new post = %+v, want %s", got, msg.ID)
	}
}

// The read cache lives as long as the engine, so it must stay bounded: a
// closed session loses its entry, and the map never grows past its limit.
func TestReadInboxCacheIsBounded(t *testing.T) {
	size := func() int {
		readInboxes.Lock()
		defer readInboxes.Unlock()
		return len(readInboxes.byKey)
	}
	readInboxes.Lock()
	clear(readInboxes.byKey)
	readInboxes.Unlock()
	t.Cleanup(func() {
		readInboxes.Lock()
		clear(readInboxes.byKey)
		readInboxes.Unlock()
	})
	open := readInboxFor("root\x00ses_a", false)
	if readInboxFor("root\x00ses_a", false) != open || size() != 1 {
		t.Fatal("an open session does not keep one cache")
	}
	if readInboxFor("root\x00ses_a", true) == open || size() != 0 {
		t.Fatalf("a closed session kept its cache entry (%d entries)", size())
	}
	for i := 0; i < maxReadInboxes+10; i++ {
		readInboxFor(fmt.Sprintf("root\x00ses_%d", i), false)
		if n := size(); n > maxReadInboxes {
			t.Fatalf("the cache holds %d entries, over the limit %d", n, maxReadInboxes)
		}
	}
}
