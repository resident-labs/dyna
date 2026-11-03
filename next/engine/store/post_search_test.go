package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSearchPostsLabelsEachPostBySource(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"r", "other"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	from := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := s.AppendEvent(tx, contract.Event{ID: "local", Time: time.Unix(1, 0), Room: "r", From: from, Body: contract.PostCreated{Text: "needle local"}}); err != nil {
			return err
		}
		source := &contract.PostSource{Room: "gone", RoomName: "gone", SessionName: "root"}
		_, err := s.AppendEvent(tx, contract.Event{ID: "cross", Time: time.Unix(2, 0), Room: "other", From: from, Body: contract.PostCreated{Text: "needle cross", Source: source}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchPosts(ctx, contract.PostSearchRequest{Limit: 10}, "needle")
	if err != nil || len(got.Posts) != 2 {
		t.Fatalf("posts=%+v err=%v", got.Posts, err)
	}
	if got.Posts[0].Source == nil || got.Posts[0].Source.SessionName != "root" || got.Posts[1].Source != nil {
		t.Fatalf("sources: cross=%+v local=%+v", got.Posts[0].Source, got.Posts[1].Source)
	}
}
