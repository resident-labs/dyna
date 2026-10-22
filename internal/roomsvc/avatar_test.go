package roomsvc

import (
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

func info(id, name string, created time.Time) subagent.Info {
	return subagent.Info{Meta: subagent.Meta{ID: id, Name: name, Room: "alpha", RoomID: "r1", Created: created}}
}

func TestAvatarsAreUniqueAndStable(t *testing.T) {
	base := time.UnixMilli(1_791_000_000_000)
	var all []subagent.Info
	for i := 0; i < 20; i++ {
		all = append(all, info(fmt.Sprintf("ses_%02d", i), fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Second)))
	}
	book := newAvatarBook(all, "r1")
	seen := map[string]bool{}
	for _, s := range all {
		e := book.byID[s.ID]
		if e == "" || seen[e] {
			t.Fatalf("%s got %q; avatars must be set and unique in a room", s.ID, e)
		}
		seen[e] = true
	}
	// A new session only adds itself; nobody else moves.
	grown := newAvatarBook(append(all, info("ses_new", "late", base.Add(time.Hour))), "r1")
	for _, s := range all {
		if grown.byID[s.ID] != book.byID[s.ID] {
			t.Fatalf("%s moved from %s to %s when a session joined", s.ID, book.byID[s.ID], grown.byID[s.ID])
		}
	}
	// Other rooms do not take part.
	other := info("ses_x", "x", base)
	other.RoomID = "r2"
	if newAvatarBook(append(all, other), "r1").byID["ses_x"] != "" {
		t.Fatal("a session in another room got an avatar here")
	}
}

func TestAvatarForPostFollowsTheSessionThatHeldTheName(t *testing.T) {
	base := time.UnixMilli(1_791_000_000_000)
	old := info("ses_old", "lead", base)
	old.Status = subagent.StatusClosed
	reused := info("ses_new", "lead", base.Add(time.Hour))
	book := newAvatarBook([]subagent.Info{old, reused}, "r1")
	if book.byID["ses_old"] == book.byID["ses_new"] {
		t.Fatal("two sessions in one room share an avatar")
	}
	if got := book.forPost("alpha:lead", base.Add(time.Minute).UnixMilli()); got != book.byID["ses_old"] {
		t.Fatalf("old post avatar = %s, want the closed session's %s", got, book.byID["ses_old"])
	}
	if got := book.forPost("alpha:lead", base.Add(2*time.Hour).UnixMilli()); got != book.byID["ses_new"] {
		t.Fatalf("new post avatar = %s, want %s", got, book.byID["ses_new"])
	}
}

func TestPoolHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range emojiPool {
		if seen[e] {
			t.Fatalf("%s is in the pool twice", e)
		}
		seen[e] = true
	}
}
