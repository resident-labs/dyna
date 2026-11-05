package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func writesStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, context.Background()
}

var issuesOnApp = contract.GitHubWrites{Repos: []contract.RepoName{"acme/app"}, Groups: []contract.GitHubWriteGroup{contract.GitHubWriteIssues}}

// room.list shows a room's githubWrites only when it is on, which the web reads as "nil means off"; a room with the setting
// cleared, or one that never had it, shows none.
func TestRoomListShowsGitHubWritesOnlyWhenOn(t *testing.T) {
	s, ctx := writesStore(t)
	for _, room := range []contract.RoomID{"room_on", "room_off", "room_cleared"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for room, writes := range map[contract.RoomID]contract.GitHubWrites{"room_on": issuesOnApp, "room_cleared": issuesOnApp} {
		if err := s.SetRoomGitHubWrites(ctx, room, writes); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRoomGitHubWrites(ctx, "room_cleared", contract.GitHubWrites{}); err != nil {
		t.Fatal(err)
	}
	// Groups with no repository, and repositories with no group, are off as well: nothing is allowed by them.
	if err := s.SetRoomGitHubWrites(ctx, "room_off", contract.GitHubWrites{Groups: issuesOnApp.Groups}); err != nil {
		t.Fatal(err)
	}
	listed, _, err := s.RoomList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shown := map[contract.RoomID]*contract.GitHubWrites{}
	for _, room := range listed.Rooms {
		shown[room.ID] = room.GitHubWrites
	}
	if got := shown["room_on"]; got == nil || !slices.Equal(got.Repos, issuesOnApp.Repos) || !slices.Equal(got.Groups, issuesOnApp.Groups) {
		t.Fatalf("on: %+v", got)
	}
	if shown["room_off"] != nil || shown["room_cleared"] != nil {
		t.Fatalf("off rooms show %+v %+v", shown["room_off"], shown["room_cleared"])
	}
}

// Only an open room's setting changes: a room being deleted, or one that is gone, keeps what it had and says it is not found.
func TestSetRoomGitHubWritesChangesOnlyAnOpenRoom(t *testing.T) {
	s, ctx := writesStore(t)
	if err := s.CreateRoom(ctx, "room_a", "a", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomGitHubWrites(ctx, "room_a", issuesOnApp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE rooms SET state='deleting' WHERE id='room_a'"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoomGitHubWrites(ctx, "room_a", contract.GitHubWrites{}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("a deleting room: %v", err)
	}
	if err := s.SetRoomGitHubWrites(ctx, "room_nope", issuesOnApp); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("a room that is not there: %v", err)
	}
	if got, err := s.RoomGitHubWrites(ctx, "room_a"); err != nil || !got.On() {
		t.Fatalf("a deleting room's setting changed: %+v %v", got, err)
	}
}

// A room's githubWrites is a column of its row, so deleting the room removes the value with it: nothing is left to apply to a
// room of the same ID, and the setting is not read back.
func TestDeletingARoomRemovesItsGitHubWrites(t *testing.T) {
	s, ctx := writesStore(t)
	for _, room := range []contract.RoomID{"room_gone", "room_stays"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetRoomGitHubWrites(ctx, room, issuesOnApp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE rooms SET state='deleting' WHERE id='room_gone'"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoom(ctx, "room_gone"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RoomGitHubWrites(ctx, "room_gone"); err == nil || got.On() {
		t.Fatalf("a deleted room still has %+v %v", got, err)
	}
	if got, err := s.RoomGitHubWrites(ctx, "room_stays"); err != nil || !got.On() {
		t.Fatalf("another room lost its setting: %+v %v", got, err)
	}
}
