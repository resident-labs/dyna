package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestEndTurnFinalReply(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readPost bool
		wait     string
	}{
		{name: "post still pending"},
		{name: "post already read", readPost: true},
		{name: "named job", readPost: true, wait: "named"},
		{name: "count-only job", readPost: true, wait: "unnamed"},
		{name: "child at work", readPost: true, wait: "child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
			s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "progress", Home: endTurnFeed, Mentions: []contract.Principal{endTurnParent}})
			now := post.Time
			dispatch := func(session contract.SessionID, turn contract.TurnID, want ...contract.EventID) {
				t.Helper()
				now = now.Add(InputBatchWindow)
				start := contract.Event{ID: contract.EventID(turn) + "-start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
				active, inputs, _, err := s.Dispatch(ctx, session, turn, start, false)
				if err != nil || active != turn || len(inputs) != len(want) {
					t.Fatalf("dispatch %s: turn %q, inputs %+v, err %v", session, active, inputs, err)
				}
				for i, input := range inputs {
					event, err := s.Event(ctx, int64(input.Seq))
					if err != nil || event.ID != want[i] {
						t.Fatalf("input %d: id %q, want %q: %v", i, event.ID, want[i], err)
					}
				}
			}
			end := func(sender contract.Principal, turn contract.TurnID, id contract.EventID, jobs int, names []contract.ShellJobID) {
				t.Helper()
				output := contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "result", ShellJobs: jobs, Jobs: names}
				event := contract.Event{ID: id, Time: now, Room: "room", From: sender, Body: contract.TurnEnded{Session: sender.Session, Turn: turn, Outcome: output.Outcome, Final: output.Final, ShellJobs: jobs, Jobs: names}}
				if err := s.EndTurn(ctx, sender.Session, output, event); err != nil {
					t.Fatal(err)
				}
			}
			if tc.readPost {
				dispatch("parent", "parent-first", post.ID)
				end(endTurnParent, "parent-first", "parent-end", 0, nil)
			}
			if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "snapshot"}}); err != nil {
				t.Fatal(err)
			}
			var jobs int
			var names []contract.ShellJobID
			grand := contract.Principal{Kind: contract.PrincipalSession, Session: "grand"}
			switch tc.wait {
			case "named":
				jobs, names = 1, []contract.ShellJobID{"job"}
			case "unnamed":
				jobs = 1
			case "child":
				if _, err := s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('grand','room','grand','p','open','',0,'child'); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('g','grand','','agent','ready','','',0,0)"); err != nil {
					t.Fatal(err)
				}
				brief := contract.Event{ID: "grand-brief", Time: now, Room: "room", From: endTurnChild, Body: contract.PostCreated{Text: "work", Home: endTurnFeed}}
				if _, err := s.Post(ctx, brief, []contract.Delivery{{To: grand, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnChild, Home: endTurnFeed}, Wake: true}}); err != nil {
					t.Fatal(err)
				}
				dispatch("grand", "grand-turn", brief.ID)
			}
			var woke []contract.SessionID
			s.SetWake(func(ids []contract.SessionID, _ []contract.Event) { woke = append(woke, ids...) })
			end(endTurnChild, "turn", "end", jobs, names)
			if _, saved, err := s.SavedTurnSections(ctx, "turn"); err != nil || saved {
				t.Fatalf("ended turn retains snapshot: saved=%v err=%v", saved, err)
			}
			if _, err := s.TurnSections(ctx, "turn", []contract.ContextSection{{Key: contract.ContextProfiles, Text: "late"}}); err == nil {
				t.Fatal("saved a snapshot after the turn ended")
			}
			events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
			if err != nil || len(events) == 0 || events[len(events)-1].ID != "end" {
				t.Fatalf("events %+v: %v", events, err)
			}
			body := events[len(events)-1].Body.(contract.TurnEnded)
			if len(body.Replies) != 1 || body.Replies[0].Posted != post.ID {
				t.Fatalf("replies %+v", body.Replies)
			}
			final := contract.EventID("end")
			if tc.wait != "" {
				if got := parentDeliveries(t, s); len(got) != 1 || slices.Contains(woke, "parent") {
					t.Fatalf("waiting turn sent more than the post: deliveries %v, wakes %v", got, woke)
				}
				input := contract.EventID("job-ended")
				if tc.wait == "child" {
					input = "grand-end"
					end(grand, "grand-turn", input, 0, nil)
				} else {
					event := contract.Event{ID: input, Time: now, Room: "room", From: endTurnChild, Body: contract.ShellJobEnded{Session: "child", Job: "job"}}
					if err := s.ShellJobEnded(ctx, "child", "job", event, false); err != nil {
						t.Fatal(err)
					}
				}
				dispatch("child", "after-wait", input)
				final = "result"
				end(endTurnChild, "after-wait", final, 0, nil)
			}
			if owed, err := s.RepliesOwed(ctx, "child"); err != nil || len(owed) != 0 {
				t.Fatalf("owed %v after finishing: %v", owed, err)
			}
			var wake bool
			if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.id=? AND d.to_id='parent'", final).Scan(&wake); err != nil || !wake || !slices.Contains(woke, "parent") {
				t.Fatalf("final wake=%v, notified=%v: %v", wake, woke, err)
			}
			if tc.readPost {
				dispatch("parent", "parent-result", final)
			} else {
				dispatch("parent", "parent-result", post.ID, final)
			}
		})
	}
}

// A post to the target from the same millisecond but before the turn started is not this
// turn's reply: it must not be picked. The final reply wakes the target either way.
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
		if len(replies) != 1 || replies[0].Posted != want || !wake {
			t.Fatalf("later post %t: replies %+v, wake %t", later, replies, wake)
		}
	}
}

// Waiting notices wake the parent without paying the reply debt.
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
		{"child's job running", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent,shell_jobs) VALUES('grand','room','grand','p','open','',0,'child',1)", 0, contract.TurnCompleted, false},
		{"child still waits itself", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('grand','room','grand','p','open','',0,'child'); INSERT INTO replies_owed(session,replies) VALUES('grand','[]')", 0, contract.TurnCompleted, false},
		{"child stopped while it owes", "INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent,model_retry_at) VALUES('grand','room','grand','p','open','',0,'child',-1); INSERT INTO replies_owed(session,replies) VALUES('grand','[]')", 0, contract.TurnCompleted, true},
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
			events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
			if err != nil || len(events) != 1 {
				t.Fatalf("events: %v %v", events, err)
			}
			body := events[0].Body.(contract.TurnEnded)
			if body.Final != "text" {
				t.Fatalf("stored text changed: %+v", body)
			}
			if tc.wake && body.WaitingNotice != "" {
				t.Fatalf("unexpected notice: %s", body.WaitingNotice)
			}
			if !tc.wake {
				label := "child grand"
				if tc.jobs > 0 {
					label = "1 job"
				}
				if body.WaitingNotice != "child's turn ended; it is waiting for "+label+"." {
					t.Fatalf("notice: %s", body.WaitingNotice)
				}
			}
			var wake bool
			if err := s.Read().QueryRowContext(ctx, "SELECT d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='turn.ended' AND d.to_id='parent'").Scan(&wake); err != nil || !wake {
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
			if _, err := s.writer.ExecContext(ctx, "DELETE FROM turns WHERE id='g'; DELETE FROM replies_owed WHERE session='grand'; DELETE FROM deliveries WHERE state='pending' AND to_id IN ('child','grand'); UPDATE sessions SET shell_jobs=0"); err != nil {
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
