package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestDeliveryReplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','room','s','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)"); err != nil {
		t.Fatal(err)
	}
	reply := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalOwner}, Home: contract.Conversation{Kind: contract.ConversationDM, Session: "s"}, ReplyTo: "post"}
	event := contract.Event{ID: "post", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "hello"}}
	delivery := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceDM, Reply: reply}
	if _, err := s.Post(ctx, event, []contract.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "started", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	turn, inputs, _, err := s.Dispatch(ctx, "s", "turn", start, false)
	if err != nil || turn != "" || len(inputs) != 0 {
		t.Fatalf("quiet delivery started turn: %q %+v %v", turn, inputs, err)
	}
	event.ID = "wake"
	delivery.Wake = true
	if _, err := s.Post(ctx, event, []contract.Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	_, inputs, _, err = s.Dispatch(ctx, "s", "turn", start, false)
	if err != nil || len(inputs) != 2 || inputs[0].Reply != reply || inputs[0].Surface != contract.SurfaceDM {
		t.Fatalf("inputs %+v: %v", inputs, err)
	}
}

func TestAdmissionRechecksIntentAndDeadline(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,want,due_at) VALUES('b','s','','agent','pending','','',0,0,'stop',0)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	old := box.Box{ID: "b", Session: "s", Size: size, State: contract.BoxPending}
	next := old
	next.State = contract.BoxCreating
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	for _, query := range []string{"", "UPDATE boxes SET want='run',due_at=101 WHERE id='b'"} {
		if query != "" {
			if _, err := s.writer.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
		ok, err := s.SetBoxLifecycle(ctx, old, next, time.Time{}, 0, event)
		if err != nil || ok {
			t.Fatalf("admitted before intent/deadline: %v %v", ok, err)
		}
	}
	if _, err := s.writer.Exec("UPDATE boxes SET due_at=100 WHERE id='b'"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.SetBoxLifecycle(ctx, old, next, time.Time{}, 0, event)
	if err != nil || !ok {
		t.Fatalf("admission failed: %v %v", ok, err)
	}
}

func TestHelloRejectsExpiredDeadline(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,due_at,failures) VALUES('b','s','','agent','starting','','',0,0,100,2)`)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := box.Size(contract.BoxSizeAgent)
	b := box.Box{ID: "b", Session: "s", Size: size, State: contract.BoxStarting}
	event := contract.Event{ID: "e", Room: "r", Time: time.UnixMilli(100), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := s.TransitionBox(ctx, b, contract.BoxReady, "", event); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired hello: %v", err)
	}
	var state string
	var due, failures int
	if err := s.Read().QueryRowContext(ctx, "SELECT state,due_at,failures FROM boxes WHERE id='b'").Scan(&state, &due, &failures); err != nil {
		t.Fatal(err)
	}
	if state != "starting" || due != 100 || failures != 2 {
		t.Fatalf("expired hello changed box: %s %d %d", state, due, failures)
	}
	event.Time = time.UnixMilli(99)
	if _, err := s.TransitionBox(ctx, b, contract.BoxReady, "", event); err != nil {
		t.Fatal(err)
	}
	if err := s.Read().QueryRowContext(ctx, "SELECT state,due_at,failures FROM boxes WHERE id='b'").Scan(&state, &due, &failures); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || due != 0 || failures != 2 {
		t.Fatalf("valid hello: %s %d %d", state, due, failures)
	}
}

func TestStartSessionIsOpen(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.UnixMilli(100)
	if err := s.CreateRoom(ctx, "r", "r", now); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEvent := contract.Event{ID: "box", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := s.StartSession(ctx, Session{ID: "s", Room: "r", Name: "s", Profile: "p", Box: "b"}, "", contract.BoxSizeAgent, "", event, boxEvent, "", false, "", 0); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id='s'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("session state = %q, want open", state)
	}
}
