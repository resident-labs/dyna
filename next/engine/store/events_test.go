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
	s, err := Open(t.TempDir(), time.Now)
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
func TestRolledBackEventIsNotPublished(t *testing.T) {
	s, err := Open(t.TempDir(), time.Now)
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
	s, err := Open(t.TempDir(), time.Now)
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
	s, err := Open(t.TempDir(), time.Now)
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
