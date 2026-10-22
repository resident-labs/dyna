package hooks

import (
	"testing"

	"dyna-agent/internal/room"
)

// A hook failure in a repo with no room creates the repo's room and posts
// there; a later failure reuses it.
func TestOwnerNoticeUsesTheRepoRoom(t *testing.T) {
	_, repo := setup(t)
	for _, text := range []string{"first failure", "second failure"} {
		if err := ownerNotice(Event{Event: "after-land", Repo: repo}, text); err != nil {
			t.Fatal(err)
		}
	}
	rooms, err := room.DefaultStore().List(true)
	if err != nil || len(rooms) != 1 || rooms[0].Repo == "" {
		t.Fatalf("rooms = %+v, %v", rooms, err)
	}
	posts, err := room.DefaultStore().Messages(rooms[0].ID)
	if err != nil || len(posts) != 2 || posts[0].Kind != "hook-failed" || posts[1].Text != "second failure" {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
}
