package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Exercise both immediate final-reply delivery and the independent failure
// notice sweep. A real 503 body has already crossed the harness boundary as a
// model_failed cause; the store must not parse or reclassify that error text.
func TestModelRetryDefersTerminalFailure(t *testing.T) {
	for _, reply := range []contract.ReplyKind{contract.ReplyNone, contract.ReplyFinal} {
		t.Run(string(reply), func(t *testing.T) {
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
			_, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('parent','r','parent','p','open','',0,NULL),('s','r','s','p','open','',0,'parent');
   INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('a','parent','','agent','ready','','',0,0),('b','s','','agent','ready','','',0,0)`)
			if err != nil {
				t.Fatal(err)
			}
			who := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
			parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
			post := contract.Event{ID: "work", Room: "r", Time: now, From: parent, Body: contract.PostCreated{Text: "work"}}
			if _, err := s.Post(ctx, post, []contract.Delivery{{To: who, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: reply, Target: parent}, Wake: true}}); err != nil {
				t.Fatal(err)
			}
			events := 0
			quiet := func(phase string, terminal bool) {
				t.Helper()
				if err := s.SendFailureNotices(ctx, now, func(id contract.RoomID, body contract.EventBody) (contract.Event, error) {
					events++
					return contract.Event{ID: contract.EventID(fmt.Sprintf("notice-%d", events)), Room: id, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
				}); err != nil {
					t.Fatal(err)
				}
				var deliveries int
				if err := s.Read().QueryRow("SELECT count(*) FROM deliveries WHERE to_id='parent'").Scan(&deliveries); err != nil {
					t.Fatal(err)
				}
				want := 0
				if terminal {
					want = 1
				}
				if deliveries != want {
					t.Errorf("%s: parent deliveries=%d, want %d", phase, deliveries, want)
				}
				rooms, _, err := s.RoomList(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if rooms.Rooms[0].Failed != want {
					t.Errorf("%s: failed sessions=%d, want %d", phase, rooms.Rooms[0].Failed, want)
				}
			}
			for i := 0; i <= 8; i++ {
				turn := contract.TurnID(fmt.Sprintf("turn-%d", i))
				started := contract.Event{ID: contract.EventID(fmt.Sprintf("start-%d", i)), Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
				got, inputs, _, err := s.Dispatch(ctx, "s", turn, started, false)
				if err != nil || got != turn || len(inputs) != 1 {
					t.Fatalf("dispatch %d: %s %d %v", i, got, len(inputs), err)
				}
				quiet("running", false)
				output := contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, Error: `503 {"error":{"code":"auth_unavailable","message":"no auth available"}}`}
				ended := contract.Event{ID: contract.EventID(fmt.Sprintf("end-%d", i)), Room: "r", Time: now, From: who, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: output.Outcome, Cause: output.Cause, Error: output.Error}}
				if err := s.EndTurn(ctx, "s", output, ended); err != nil {
					t.Fatal(err)
				}
				// Replaying a committed output must neither reschedule nor notify twice.
				if err := s.EndTurn(ctx, "s", output, ended); err != nil {
					t.Fatal(err)
				}
				if i == 8 {
					quiet("exhausted", true)
					break
				}
				quiet("backoff", false)
				var due int64
				if err := s.Read().QueryRow("SELECT model_retry_at FROM sessions WHERE id='s'").Scan(&due); err != nil || due <= now.UnixMilli() {
					t.Fatalf("retry deadline %d: %v", due, err)
				}
				if i == 0 {
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = Open(dir)
					if err != nil {
						t.Fatal(err)
					}
					quiet("after engine restart", false)
				}
				now = time.UnixMilli(due)
				if err := s.WakeModelRetries(ctx, now); err != nil {
					t.Fatal(err)
				}
				quiet("queued before dispatch", false)
			}
			// A new successful turn clears both the finite counter and the failure view.
			post.ID = "new-work"
			post.Time = now.Add(time.Minute)
			if _, err := s.Post(ctx, post, []contract.Delivery{{To: who, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
				t.Fatal(err)
			}
			started := contract.Event{ID: "recovery-start", Room: "r", Time: post.Time, From: parent, Body: contract.TurnStarted{}}
			if _, _, _, err := s.Dispatch(ctx, "s", "recovery", started, false); err != nil {
				t.Fatal(err)
			}
			ended := contract.Event{ID: "recovery-end", Room: "r", Time: post.Time, From: who, Body: contract.TurnEnded{Session: "s", Turn: "recovery", Outcome: contract.TurnCompleted}}
			if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "recovery", Outcome: contract.TurnCompleted}, ended); err != nil {
				t.Fatal(err)
			}
			var retries, deadline int64
			if err := s.Read().QueryRow("SELECT model_retries,model_retry_at FROM sessions WHERE id='s'").Scan(&retries, &deadline); err != nil || retries != 0 || deadline != 0 {
				t.Fatalf("recovery counters %d %d: %v", retries, deadline, err)
			}
		})
	}
}
