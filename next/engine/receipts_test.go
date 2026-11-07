package engine

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestFeedReceiptReadSecurity(t *testing.T) {
	s, r, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	post, p := s.Post(ctx, owner, contract.PostRequest{Room: r, Home: feedHome, Text: "hello", Mentions: []string{"worker"}})
	if p != nil {
		t.Fatal(p)
	}
	event, err := s.store.EventInRoom(ctx, r, post.Event)
	if err != nil {
		t.Fatal(err)
	}
	req := contract.EventsListRequest{Room: r, ReceiptSeqs: []int64{event.Seq}}
	got, p := s.EventsList(ctx, owner, req)
	if p != nil || len(got.Receipts[event.Seq]) != 1 || got.Receipts[event.Seq][0].Session != worker {
		t.Fatalf("%+v %v", got, p)
	}
	other, p := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if p != nil {
		t.Fatal(p)
	}
	req.Room = other.Room
	got, p = s.EventsList(ctx, owner, req)
	if p != nil || len(got.Receipts) != 0 {
		t.Fatalf("foreign event leaked %+v %v", got, p)
	}
	if _, p = s.EventsList(ctx, boxCaller(worker), req); p == nil {
		t.Fatal("foreign room allowed")
	}
	// DM posts are not exposed by this feed-only read contract.
	dm, p := s.Post(ctx, owner, contract.PostRequest{Room: r, Home: dmHome(worker), Text: "private"})
	if p != nil {
		t.Fatal(p)
	}
	private, err := s.store.EventInRoom(ctx, r, dm.Event)
	if err != nil {
		t.Fatal(err)
	}
	got, p = s.EventsList(ctx, owner, contract.EventsListRequest{Room: r, ReceiptSeqs: []int64{private.Seq}})
	if p != nil || len(got.Receipts) != 0 {
		t.Fatalf("DM receipt %+v %v", got, p)
	}
	foreign, p := s.Post(ctx, owner, contract.PostRequest{Room: other.Room, Home: feedHome, Text: "foreign"})
	if p != nil {
		t.Fatal(p)
	}
	for _, caller := range []contract.Principal{owner, boxCaller(worker)} {
		got, p = s.EventsList(ctx, caller, contract.EventsListRequest{Room: r, ReceiptSeqs: []int64{post.Seq, private.Seq, foreign.Seq, -1, 99999999}})
		if p != nil || len(got.Receipts) != 1 || len(got.Receipts[post.Seq]) != 1 {
			t.Fatalf("mixed feed, DM and foreign lookup: %+v %v", got, p)
		}
	}
}
