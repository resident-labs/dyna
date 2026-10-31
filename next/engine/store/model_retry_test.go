package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestModelFailureRetries(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	principal := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
	post := func(id string) {
		t.Helper()
		ev := contract.Event{ID: contract.EventID(id), Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
		_, err := s.Post(ctx, ev, []contract.Delivery{{To: principal, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	finish := func(i int, outcome contract.TurnOutcome, cause contract.FailureCause) {
		t.Helper()
		turn := contract.TurnID(fmt.Sprintf("turn-%d", i))
		started := contract.Event{ID: contract.EventID(fmt.Sprintf("start-%d", i)), Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
		got, inputs, _, err := s.Dispatch(ctx, "s", turn, started, false)
		if err != nil || got != turn || len(inputs) == 0 {
			t.Fatalf("dispatch %d: %q %d %v", i, got, len(inputs), err)
		}
		ended := contract.Event{ID: contract.EventID(fmt.Sprintf("end-%d", i)), Room: "r", Time: now, From: principal, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: outcome, Cause: cause}}
		if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: turn, Outcome: outcome, Cause: cause}, ended); err != nil {
			t.Fatal(err)
		}
	}
	post("initial")
	finish(100, contract.TurnFailed, contract.CauseModelFailed)
	post("owner-intervenes")
	started := contract.Event{ID: "manual-start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	turn, inputs, _, err := s.Dispatch(ctx, "s", "manual-turn", started, false)
	if err != nil || turn != "manual-turn" || len(inputs) != 2 {
		t.Fatalf("manual dispatch: %q %d %v", turn, len(inputs), err)
	}
	// A timer that becomes due while this new turn runs cannot wake it again.
	due := now.Add(5 * time.Minute)
	if err := s.WakeModelRetries(ctx, due); err != nil {
		t.Fatal(err)
	}
	ended := contract.Event{ID: "manual-end", Room: "r", Time: now, From: principal, Body: contract.TurnEnded{Session: "s", Turn: turn, Outcome: contract.TurnCompleted}}
	if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}, ended); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeModelRetries(ctx, due); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND state='pending' AND wake=1").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("old timer woke completed work: %d %v", pending, err)
	}
	post("again")
	delays := []time.Duration{5, 10, 20, 40, 60, 60, 60, 60}
	for i, mins := range delays {
		finish(i, contract.TurnFailed, contract.CauseModelFailed)
		var retryAt int64
		var retries int
		if err := s.Read().QueryRowContext(ctx, "SELECT model_retry_at,model_retries FROM sessions WHERE id='s'").Scan(&retryAt, &retries); err != nil {
			t.Fatal(err)
		}
		due := now.Add(mins * time.Minute)
		if retryAt != due.UnixMilli() || retries != i+1 {
			t.Fatalf("retry %d: %d %d", i, retryAt, retries)
		}
		if err := s.WakeModelRetries(ctx, due.Add(-time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		var wake bool
		if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id='s' AND state='pending'").Scan(&wake); err != nil || wake {
			t.Fatalf("early wake %v: %v", wake, err)
		}
		if i == 0 {
			if _, err := s.writer.Exec("UPDATE rooms SET paused_at=1"); err != nil {
				t.Fatal(err)
			}
			if err := s.WakeModelRetries(ctx, due); err != nil {
				t.Fatal(err)
			}
			if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id='s' AND state='pending'").Scan(&wake); err != nil || wake {
				t.Fatalf("paused wake %v: %v", wake, err)
			}
			if _, err := s.writer.Exec("UPDATE rooms SET paused_at=NULL"); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.WakeModelRetries(ctx, due); err != nil {
			t.Fatal(err)
		}
		if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id='s' AND state='pending'").Scan(&wake); err != nil || !wake {
			t.Fatalf("due wake %v: %v", wake, err)
		}
		now = due
	}
	finish(8, contract.TurnFailed, contract.CauseModelFailed)
	if err := s.WakeModelRetries(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var wake bool
	if err := s.Read().QueryRowContext(ctx, "SELECT wake FROM deliveries WHERE to_id='s' AND state='pending'").Scan(&wake); err != nil || wake {
		t.Fatalf("limit wake %v: %v", wake, err)
	}
	post("new")
	finish(9, contract.TurnCompleted, "")
	var retryAt int64
	var retries int
	if err := s.Read().QueryRowContext(ctx, "SELECT model_retry_at,model_retries FROM sessions WHERE id='s'").Scan(&retryAt, &retries); err != nil || retryAt != 0 || retries != 0 {
		t.Fatalf("completed: %d %d %v", retryAt, retries, err)
	}
}
