package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestCancelledModelRetryIsNotTerminal(t *testing.T) {
	for _, reply := range []contract.ReplyKind{contract.ReplyNone, contract.ReplyFinal} {
		for _, phase := range []string{"before due", "queued", "active stopped", "legacy active stopped", "active failure races Stop", "active non-model failure after Stop", "non-model failure after cancellation", "exhaustion remains terminal"} {
			t.Run(string(reply)+"/"+phase, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				s, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { s.Close() }()
				now := time.Unix(1000, 0)
				if err := s.CreateRoom(ctx, "r", "r", now); err != nil {
					t.Fatal(err)
				}
				if _, err := s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('parent','r','parent','p','open','',0,NULL),('s','r','s','p','open','',0,'parent');
     INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('a','parent','','agent','ready','','',0,0),('b','s','','agent','ready','','',0,0)`); err != nil {
					t.Fatal(err)
				}
				who := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
				parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
				post := func(id string, kind contract.ReplyKind) {
					t.Helper()
					event := contract.Event{ID: contract.EventID(id), Room: "r", Time: now, From: parent, Body: contract.PostCreated{Text: "work"}}
					if _, err := s.Post(ctx, event, []contract.Delivery{{To: who, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: kind, Target: parent}, Wake: true}}); err != nil {
						t.Fatal(err)
					}
				}
				dispatch := func(i int) {
					t.Helper()
					turn := contract.TurnID(fmt.Sprintf("turn-%d", i))
					event := contract.Event{ID: contract.EventID(fmt.Sprintf("start-%d", i)), Room: "r", Time: now, From: parent, Body: contract.TurnStarted{}}
					got, inputs, _, err := s.Dispatch(ctx, "s", turn, event, false)
					if err != nil || got != turn || len(inputs) == 0 {
						t.Fatalf("dispatch %d: %s %d %v", i, got, len(inputs), err)
					}
				}
				finish := func(i int, outcome contract.TurnOutcome, cause contract.FailureCause) {
					t.Helper()
					turn := contract.TurnID(fmt.Sprintf("turn-%d", i))
					output := contract.TurnOutputRequest{Turn: turn, Outcome: outcome, Cause: cause}
					if outcome == contract.TurnFailed {
						output.Error = "fixture model or harness failure"
					}
					event := contract.Event{ID: contract.EventID(fmt.Sprintf("end-%d", i)), Room: "r", Time: now, From: who, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: outcome, Cause: cause, Error: output.Error}}
					if err := s.EndTurn(ctx, "s", output, event); err != nil {
						t.Fatal(err)
					}
				}
				notices := 0
				terminal := func(label string, want int, replies ...int) {
					t.Helper()
					if err := s.SendFailureNotices(ctx, now, func(id contract.RoomID, body contract.EventBody) (contract.Event, error) {
						notices++
						return contract.Event{ID: contract.EventID(fmt.Sprintf("notice-%d", notices)), Room: id, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
					}); err != nil {
						t.Fatal(err)
					}
					var count int
					if err := s.Read().QueryRow("SELECT count(*) FROM deliveries WHERE to_id='parent'").Scan(&count); err != nil {
						t.Fatal(err)
					}
					rooms, _, err := s.RoomList(ctx)
					if err != nil {
						t.Fatal(err)
					}
					wantReplies := want
					if len(replies) > 0 {
						wantReplies = replies[0]
					}
					if count != wantReplies || rooms.Rooms[0].Failed != want {
						t.Errorf("%s: parent deliveries=%d want %d, failed sessions=%d want %d", label, count, wantReplies, rooms.Rooms[0].Failed, want)
					}
				}
				wake := func() {
					t.Helper()
					var due int64
					if err := s.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id='s'").Scan(&due); err != nil || due <= 0 {
						t.Fatalf("deadline %d: %v", due, err)
					}
					now = time.UnixMilli(due)
					if err := s.WakeModelRetries(ctx, now); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "legacy active stopped" {
					post("old-work", contract.ReplyNone)
					dispatch(100)
					finish(100, contract.TurnFailed, contract.CauseModelFailed)
					now = now.Add(time.Minute)
					post("recovery-work", contract.ReplyNone)
					dispatch(101)
					finish(101, contract.TurnCompleted, "")
				}
				now = now.Add(time.Minute)
				post("work", reply)
				dispatch(0)
				finish(0, contract.TurnFailed, contract.CauseModelFailed)
				terminal("initial backoff", 0)
				if phase == "exhaustion remains terminal" {
					for i := 1; i <= 8; i++ {
						wake()
						dispatch(i)
						finish(i, contract.TurnFailed, contract.CauseModelFailed)
					}
					terminal("exhausted", 1)
					if stopping, err := s.RequestTurnStop(ctx, "s", now); err != nil || stopping {
						t.Fatalf("exhausted stop: %v %v", stopping, err)
					}
					terminal("Stop must not conceal true exhaustion", 1)
					now = now.Add(time.Minute)
					post("work-after-exhaustion", contract.ReplyNone)
					dispatch(9)
					terminal("new active work does not hide exhaustion", 1)
					if stopping, err := s.RequestTurnStop(ctx, "s", now); err != nil || !stopping {
						t.Fatalf("stop new work: %v %v", stopping, err)
					}
					finish(9, contract.TurnStopped, "")
					// The old ReplyFinal input receives a normal Stop acknowledgement,
					// not another failure, when the owner resumes and stops it.
					wantReplies := 1
					if reply == contract.ReplyFinal {
						wantReplies++
					}
					terminal("stopped new work does not hide exhaustion", 1, wantReplies)
					return
				}
				if phase == "queued" || phase == "active stopped" || phase == "legacy active stopped" || phase == "active failure races Stop" || phase == "active non-model failure after Stop" {
					wake()
				}
				active := phase == "active stopped" || phase == "legacy active stopped" || phase == "active failure races Stop" || phase == "active non-model failure after Stop"
				if active {
					dispatch(1)
				}
				if phase == "legacy active stopped" {
					// Version 96 dispatched active retries with a zero timer.
					if _, err := s.writer.Exec("UPDATE sessions SET model_retry_at=0 WHERE id='s'"); err != nil {
						t.Fatal(err)
					}
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = Open(dir)
					if err != nil {
						t.Fatal(err)
					}
					terminal("legacy active retry after reopen", 0)
				}
				if stopping, err := s.RequestTurnStop(ctx, "s", now); err != nil || !stopping {
					t.Fatalf("stop: %v %v", stopping, err)
				}
				if phase == "active stopped" || phase == "legacy active stopped" {
					finish(1, contract.TurnStopped, "")
				}
				if phase == "active failure races Stop" {
					finish(1, contract.TurnFailed, contract.CauseModelFailed)
				}
				wantTerminal := 0
				if phase == "active non-model failure after Stop" {
					finish(1, contract.TurnFailed, contract.CauseHarnessFailed)
					wantTerminal = 1
				}
				terminal("cancelled model attempt or genuine non-model failure", wantTerminal)
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				terminal("cancelled after restart", wantTerminal)
				if err := s.WakeModelRetries(ctx, now.Add(24*time.Hour)); err != nil {
					t.Fatal(err)
				}
				var wakes int
				if err := s.Read().QueryRow("SELECT count(*) FROM deliveries WHERE to_id='s' AND wake=1 AND state='pending'").Scan(&wakes); err != nil || wakes != 0 {
					t.Fatalf("cancelled inputs revived: %d %v", wakes, err)
				}
				var outcome string
				if err := s.Read().QueryRow("SELECT outcome FROM turns WHERE id='turn-0'").Scan(&outcome); err != nil || outcome != "failed" {
					t.Fatalf("attempt history changed: %q %v", outcome, err)
				}
				var facts int
				if err := s.Read().QueryRow("SELECT count(*) FROM events WHERE id='end-0' AND json_extract(body,'$.outcome')='failed'").Scan(&facts); err != nil || facts != 1 {
					t.Fatalf("attempt event changed: %d %v", facts, err)
				}
				if phase == "non-model failure after cancellation" {
					now = now.Add(time.Minute)
					post("new-work", reply)
					dispatch(2)
					finish(2, contract.TurnFailed, contract.CauseHarnessFailed)
					terminal("genuine non-model failure", 1)
					now = now.Add(time.Minute)
					post("work-after-harness-failure", contract.ReplyNone)
					dispatch(3)
					terminal("new active work does not hide non-model failure", 1)
					if stopping, err := s.RequestTurnStop(ctx, "s", now); err != nil || !stopping {
						t.Fatalf("stop new work: %v %v", stopping, err)
					}
					finish(3, contract.TurnStopped, "")
					wantReplies := 1
					if reply == contract.ReplyFinal {
						wantReplies++
					}
					terminal("stopped new work does not hide non-model failure", 1, wantReplies)
				}
			})
		}
	}
}
