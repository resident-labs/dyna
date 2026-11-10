package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestEndTurnFinalReply(t *testing.T) {
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
	_, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('parent','room','parent','p','open','',0,NULL),('child','room','child','p','open','',0,'parent'); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','child','','agent','ready','','',0,0)")
	if err != nil {
		t.Fatal(err)
	}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	child := contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	event := contract.Event{ID: "brief", Time: now, Room: "room", From: parent, Body: contract.PostCreated{Text: "brief", Home: feed}}
	d := contract.Delivery{To: child, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}, Wake: true}
	if _, err := s.Post(ctx, event, []contract.Delivery{d}); err != nil {
		t.Fatal(err)
	}
	start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "snapshot"}}); err != nil {
		t.Fatal(err)
	}
	post := contract.Event{ID: "reply", Time: now, Room: "room", From: child, Body: contract.PostCreated{Text: "done", Home: feed, Mentions: []contract.Principal{parent}}}
	if _, err := s.Post(ctx, post, nil); err != nil {
		t.Fatal(err)
	}
	output := contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}
	end := contract.Event{ID: "end", Time: now, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}}
	if err := s.EndTurn(ctx, "child", output, end); err != nil {
		t.Fatal(err)
	}
	if _, saved, err := s.SavedTurnSections(ctx, "turn"); err != nil || saved {
		t.Fatalf("ended turn retains snapshot: saved=%v err=%v", saved, err)
	}
	if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "late"}}); err == nil {
		t.Fatal("saved a snapshot after the turn ended")
	}
	events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events %+v %v", events, err)
	}
	body := events[0].Body.(contract.TurnEnded)
	if len(body.Replies) != 1 || body.Replies[0].Posted != "reply" {
		t.Fatalf("replies %+v", body.Replies)
	}
	var wake bool
	if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE seq=? AND to_id='parent'", events[0].Seq).Scan(&wake); err != nil || wake {
		t.Fatalf("wake %v: %v", wake, err)
	}
}

// A post to the target from the same millisecond but before the turn started is not this
// turn's reply: it must not be picked, and it must not make the final reply quiet.
func TestEndTurnIgnoresPostBeforeTurnStart(t *testing.T) {
	for _, later := range []bool{false, true} {
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
		if _, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('parent','room','parent','p','open','',0,NULL),('child','room','child','p','open','',0,'parent'); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','child','','agent','ready','','',0,0)"); err != nil {
			t.Fatal(err)
		}
		parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
		child := contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
		feed := contract.Conversation{Kind: contract.ConversationFeed}
		post := func(id contract.EventID, from contract.Principal, deliveries []contract.Delivery) {
			event := contract.Event{ID: id, Time: now, Room: "room", From: from, Body: contract.PostCreated{Text: string(id), Home: feed, Mentions: []contract.Principal{parent}}}
			if _, err := s.Post(ctx, event, deliveries); err != nil {
				t.Fatal(err)
			}
		}
		post("brief", parent, []contract.Delivery{{To: child, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}, Wake: true}})
		post("early", child, nil)
		start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
		if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
			t.Fatal(err)
		}
		want := contract.EventID("")
		if later {
			want = "later"
			post(want, child, nil)
		}
		end := contract.Event{ID: "end", Time: now, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}}
		if err := s.EndTurn(ctx, "child", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnCompleted, Final: "result"}, end); err != nil {
			t.Fatal(err)
		}
		events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
		if err != nil || len(events) != 1 {
			t.Fatalf("events %+v %v", events, err)
		}
		replies := events[0].Body.(contract.TurnEnded).Replies
		var wake bool
		if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE seq=? AND to_id='parent'", events[0].Seq).Scan(&wake); err != nil {
			t.Fatal(err)
		}
		if len(replies) != 1 || replies[0].Posted != want || wake == later {
			t.Fatalf("later post %t: replies %+v, wake %t", later, replies, wake)
		}
	}
}

// A completed turn wakes the session it answers only when nothing will start its own session again.
func TestEndTurnReplyWaitsWhileTheSessionStillWaits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   string
		jobs    int
		outcome contract.TurnOutcome
		wake    bool
	}{
		{"finished", "", 0, contract.TurnCompleted, true},
		{"job running", "", 1, contract.TurnCompleted, false},
		{"child at work", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('grand','room','grand','p','open','',0,'child'); INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('g','grand',1,'','',0,0,0,0)", 0, contract.TurnCompleted, false},
		{"closed child", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent,closed_at) VALUES('grand','room','grand','p','closed','',0,'child',1); INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('g','grand',1,'','',0,0,0,0)", 0, contract.TurnCompleted, true},
		{"child briefed", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('grand','room','grand','p','open','',0,'child'); INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES('session','grand',(SELECT seq FROM events WHERE id='start'),'engine','none','','','','',1,'pending')", 0, contract.TurnCompleted, false},
		{"own input unread", "INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES('session','child',(SELECT seq FROM events WHERE id='start'),'engine','none','','','','',1,'pending')", 0, contract.TurnCompleted, true},
		{"failed with a job running", "", 1, contract.TurnFailed, true},
		{"stopped with a job running", "", 1, contract.TurnStopped, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('parent','room','parent','p','open','',0,NULL),('child','room','child','p','open','',0,'parent'); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','child','','agent','ready','','',0,0)"); err != nil {
				t.Fatal(err)
			}
			parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
			child := contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
			feed := contract.Conversation{Kind: contract.ConversationFeed}
			brief := contract.Event{ID: "brief", Time: now, Room: "room", From: parent, Body: contract.PostCreated{Text: "brief", Home: feed}}
			if _, err := s.Post(ctx, brief, []contract.Delivery{{To: child, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}, Wake: true}}); err != nil {
				t.Fatal(err)
			}
			start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
			if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
				t.Fatal(err)
			}
			if tc.setup != "" {
				if _, err := s.writer.ExecContext(ctx, tc.setup); err != nil {
					t.Fatal(err)
				}
			}
			output := contract.TurnOutputRequest{Turn: "turn", Outcome: tc.outcome, Final: "text", ShellJobs: tc.jobs}
			end := contract.Event{ID: "end", Time: now, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: tc.outcome, Final: "text", ShellJobs: tc.jobs}}
			if err := s.EndTurn(ctx, "child", output, end); err != nil {
				t.Fatal(err)
			}
			var wake bool
			if err := s.Read().QueryRowContext(ctx, "SELECT d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='turn.ended' AND d.to_id='parent'").Scan(&wake); err != nil || wake != tc.wake {
				t.Fatalf("wake %v, want %v: %v", wake, tc.wake, err)
			}
			if owed, err := s.RepliesOwed(ctx, "child"); err != nil || (len(owed) == 1) == tc.wake {
				t.Fatalf("owed %v after a turn that woke the parent %v: %v", owed, tc.wake, err)
			}
			// What the session waited for goes without an input: the pass wakes it only then, and once.
			later := now.Add(5 * time.Minute)
			pending := func() (n int) {
				if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id='child' AND state='pending' AND wake=1").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			before := pending()
			if err := s.WakeOwedReplies(ctx, later); err != nil || pending() != before {
				t.Fatalf("woke a session that still waits or owes nothing: %d to %d, %v", before, pending(), err)
			}
			if _, err := s.writer.ExecContext(ctx, "DELETE FROM turns WHERE id='g'; DELETE FROM deliveries WHERE state='pending' AND to_id IN ('child','grand'); UPDATE sessions SET shell_jobs=0"); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := s.WakeOwedReplies(ctx, later); err != nil {
					t.Fatal(err)
				}
			}
			if got := pending(); (got == 1) == tc.wake || got > 1 {
				t.Fatalf("%d notices for a session whose first turn woke the parent %v", got, tc.wake)
			}
			if tc.wake {
				return
			}
			again := contract.Event{ID: "start2", Time: later, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
			if active, _, _, err := s.Dispatch(ctx, "child", "turn2", again, false); err != nil || active != "turn2" {
				t.Fatal(err)
			}
			end2 := contract.Event{ID: "end2", Time: later, Room: "room", From: child, Body: contract.TurnEnded{Session: "child", Turn: "turn2", Outcome: contract.TurnCompleted, Final: "result"}}
			if err := s.EndTurn(ctx, "child", contract.TurnOutputRequest{Turn: "turn2", Outcome: contract.TurnCompleted, Final: "result"}, end2); err != nil {
				t.Fatal(err)
			}
			var answered int
			if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id='end2' AND d.to_id='parent' AND d.wake=1").Scan(&answered); err != nil || answered != 1 {
				t.Fatalf("the turn that paid woke the parent %d times: %v", answered, err)
			}
			if owed, err := s.RepliesOwed(ctx, "child"); err != nil || len(owed) != 0 {
				t.Fatalf("reply still owed: %v %v", owed, err)
			}
		})
	}
}
