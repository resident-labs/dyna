package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestEventPublishedAfterCommit(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	event := contract.Event{ID: "ev_1", Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "hello"}}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, event)
		select {
		case <-ch:
			t.Fatal("published before commit")
		default:
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.Seq != 1 || got.Body.(contract.PostCreated).Text != "hello" {
		t.Fatalf("got %#v", got)
	}
}
func TestEventsPublishInCommitOrder(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	from := contract.Principal{Kind: contract.PrincipalEngine}
	commits := [][]contract.EventID{{"ev_1"}, {"ev_2", "ev_3"}, {"ev_4"}}
	for _, ids := range commits {
		err := s.Write(context.Background(), func(tx *sql.Tx) error {
			for _, id := range ids {
				if _, err := s.AppendEvent(tx, contract.Event{ID: id, Time: time.UnixMilli(10), From: from, Body: contract.PostCreated{Text: string(id)}}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []contract.EventID{"ev_1", "ev_2", "ev_3", "ev_4"}
	for i, id := range want {
		select {
		case got := <-ch:
			if got.Seq != int64(i+1) || got.ID != id {
				t.Fatalf("event %d: got seq %d id %s", i, got.Seq, got.ID)
			}
		default:
			t.Fatalf("event %s was not published", id)
		}
	}
}
func TestRolledBackEventIsNotPublished(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.Subscribe()
	defer cancel()
	want := errors.New("abort")
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, contract.Event{ID: "ev_1", Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "hello"}})
		if err != nil {
			return err
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	select {
	case e := <-ch:
		t.Fatalf("published rolled back event: %#v", e)
	default:
	}
	_ = s.Close()
}
func TestMachineEventReplay(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, contract.Event{ID: "ev_machine", Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "machine"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(context.Background(), "", 0, 10, nil)
	if err != nil || len(events) != 1 || events[0].ID != "ev_machine" {
		t.Fatalf("events %v, error %v", events, err)
	}
}

func TestEventsReplay(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES ('room_1','r',0,1,1)"); err != nil {
			return err
		}
		_, err := s.AppendEvent(tx, contract.Event{ID: "ev_1", Room: "room_1", Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalSession, Session: "ses_1"}, Body: contract.PostCreated{Text: "hello"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Events(context.Background(), "room_1", 0, 10, []contract.EventType{"post.created"})
	if err != nil || len(got) != 1 || got[0].From.Session != "ses_1" || got[0].Body.(contract.PostCreated).Text != "hello" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestEventBySequenceReturnsAnyRoomEvent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range []string{"room_1", "room_2"} {
			if _, err := tx.Exec("INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES (?,?,0,1,1)", id, id); err != nil {
				return err
			}
		}
		if _, err := s.AppendEvent(tx, contract.Event{ID: "ev_1", Room: "room_1", Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "first"}}); err != nil {
			return err
		}
		_, err := s.AppendEvent(tx, contract.Event{ID: "ev_2", Room: "room_2", Time: time.UnixMilli(20), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: "second"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Event(context.Background(), 2)
	if err != nil || got.ID != "ev_2" || got.Room != "room_2" || got.Body.(contract.PostCreated).Text != "second" {
		t.Fatalf("event %#v, error %v", got, err)
	}
	if _, err := s.Event(context.Background(), 3); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing event error %v", err)
	}
}

func TestEventsBefore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range []contract.EventID{"a", "b", "c", "d"} {
			if _, err := s.AppendEvent(tx, contract.Event{ID: id, Time: time.UnixMilli(10), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.PostCreated{Text: string(id)}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.EventsPage(context.Background(), "", 0, 4, 2, nil, "")
	if err != nil || len(events) != 2 || events[0].ID != "b" || events[1].ID != "c" {
		t.Fatalf("events %v, error %v", events, err)
	}
}

func TestEventsPageDM(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "s"}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	bodies := []contract.EventBody{
		contract.PostCreated{Text: "feed", Home: contract.Conversation{Kind: contract.ConversationFeed}},
		contract.PostCreated{Text: "ask", Home: dm},
		contract.TurnStarted{Session: "s", Turn: "t"},
		contract.TurnEnded{Session: "s", Turn: "t", Final: "answer", Replies: []contract.FinalReply{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: dm}}}},
		contract.PostCreated{Text: "other", Home: contract.Conversation{Kind: contract.ConversationDM, Session: "x"}},
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for i, body := range bodies {
			if _, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID(rune('a' + i)), Time: time.UnixMilli(10), From: owner, Body: body}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(stage string) {
		events, err := s.EventsPage(ctx, "", 0, 6, 10, nil, "s")
		if err != nil || len(events) != 2 || events[0].Seq != 2 || events[1].Seq != 4 {
			t.Fatalf("%s: events %+v, error %v", stage, events, err)
		}
	}
	check("append")
}

func TestStopAndPauseReachTheirViews(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", time.UnixMilli(1)); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for i, body := range []contract.EventBody{contract.TurnEnded{Session: "s", Turn: "t", Outcome: contract.TurnStopped}, contract.RoomPaused{}, contract.RoomResumed{}} {
			if _, err := s.AppendEvent(tx, contract.Event{ID: contract.EventID(rune('a' + i)), Time: time.UnixMilli(10), Room: "room", From: owner, Body: body}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := s.EventsPage(ctx, "room", 0, 0, 10, []contract.EventType{"turn.ended"}, "s")
	if err != nil || len(dm) != 1 {
		t.Fatalf("a stopped turn belongs to its DM: events %+v, error %v", dm, err)
	}
	all, err := s.OwnerEvents(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var paused int
	for _, event := range all {
		if kind := event.Body.EventType(); kind == "room.paused" || kind == "room.resumed" {
			paused++
		}
	}
	if paused != 2 {
		t.Fatalf("the owner stream carries a room's pause: events %+v", all)
	}
}

func TestPairedOwnerEventReplay(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	from := contract.Principal{Kind: contract.PrincipalOwner, Device: "dev_phone"}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := s.AppendEvent(tx, contract.Event{ID: "ev_phone", Time: time.UnixMilli(10), From: from, Body: contract.PostCreated{Text: "hello"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "", 0, 10, nil)
	if err != nil || len(events) != 1 || events[0].From != from {
		t.Fatalf("events %+v, error %v", events, err)
	}
}
