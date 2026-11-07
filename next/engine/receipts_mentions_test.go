package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestReceiptClosedMentionAuthorization(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, p := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if p != nil {
		t.Fatal(p)
	}
	foreign, p := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: other.Room, Name: "foreign", Profile: "test", Goal: "goal", Brief: "brief"})
	if p != nil {
		t.Fatal(p)
	}
	live, p := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "live", Profile: "test", Goal: "goal", Brief: "brief"})
	if p != nil {
		t.Fatal(p)
	}
	// Group-expanded mentions use this same durable principal list. Repeated
	// mentions must not create repeated tooltip entries. Implicit closed recipients
	// have no historical association after close and must not be invented.
	mentions := []contract.Principal{{Kind: contract.PrincipalSession, Session: worker}, {Kind: contract.PrincipalSession, Session: foreign.Session}, {Kind: contract.PrincipalSession, Session: worker}, {Kind: contract.PrincipalSession, Session: "missing"}}
	seq, err := s.store.Post(ctx, contract.Event{ID: "mentioned", Room: room, Time: time.Now(), From: owner, Body: contract.PostCreated{Home: feedHome, Text: "mentions", Mentions: mentions}}, []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: live.Session}, Wake: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		id   contract.SessionID
		room contract.RoomID
	}{{worker, room}, {foreign.Session, other.Room}} {
		if err := s.store.CloseSession(ctx, target.id, contract.Event{ID: contract.EventID("close-" + string(target.id)), Room: target.room, Time: time.Now(), From: owner, Body: contract.SessionClosed{Session: target.id}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, caller := range []contract.Principal{owner, boxCaller(live.Session)} {
		got, p := s.EventsList(ctx, caller, contract.EventsListRequest{Room: room, ReceiptSeqs: []int64{seq}})
		if p != nil {
			t.Fatal(p)
		}
		want := 3
		if caller.Kind == contract.PrincipalSession {
			want = 2
		}
		if len(got.Receipts[seq]) != want {
			t.Fatalf("mention receipts: %+v", got)
		}
		for _, entry := range got.Receipts[seq] {
			if entry.Session == live.Session {
				continue
			}
			if caller.Kind == contract.PrincipalSession && entry.Session == foreign.Session {
				t.Fatalf("foreign mention leaked: %+v", entry)
			}
			if !entry.Closed || entry.State != "closed" || entry.BatchDeadline != nil {
				t.Fatalf("closed mention: %+v", entry)
			}
		}
	}
}
