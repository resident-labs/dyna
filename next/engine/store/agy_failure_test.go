package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// The agy harness ends a 403 TOS_VIOLATION, VALIDATION_REQUIRED or any other non-retryable Google failure as harness_failed with
// Google's own message. The engine then makes no retry and schedules no wake: the session waits for a new input.
func TestAgyNonRetryableFailureSchedulesNoRetry(t *testing.T) {
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
	post := contract.Event{ID: "post", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
	if _, err := s.Post(ctx, post, []contract.Delivery{{To: principal, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		t.Fatal(err)
	}
	started := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if turn, _, _, err := s.Dispatch(ctx, "s", "turn", started, false); err != nil || turn != "turn" {
		t.Fatalf("dispatch: %q %v", turn, err)
	}
	message := "403 PERMISSION_DENIED: TOS_VIOLATION"
	ended := contract.Event{ID: "end", Room: "r", Time: now, From: principal, Body: contract.TurnEnded{Session: "s", Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: message}}
	if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseHarnessFailed, Error: message}, ended); err != nil {
		t.Fatal(err)
	}
	var retryAt int64
	var retries int
	if err := s.Read().QueryRowContext(ctx, "SELECT model_retry_at,model_retries FROM sessions WHERE id='s'").Scan(&retryAt, &retries); err != nil || retryAt > 0 || retries != 0 {
		t.Fatalf("a retry was scheduled: at %d, retries %d, %v", retryAt, retries, err)
	}
	if err := s.WakeModelRetries(ctx, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var woken int
	if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND state='pending' AND wake=1").Scan(&woken); err != nil || woken != 0 {
		t.Fatalf("a wake was scheduled: %d %v", woken, err)
	}
}

// A usage limit that resets in days wakes the session once, at the reset, and not before: the reset the harness reports is kept
// as given (up to MaxModelQuotaResetSeconds), with no ladder step in between.
func TestAgyQuotaFailureWakesOnceAtTheReset(t *testing.T) {
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
	post := contract.Event{ID: "post", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
	if _, err := s.Post(ctx, post, []contract.Delivery{{To: principal, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		t.Fatal(err)
	}
	started := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if turn, _, _, err := s.Dispatch(ctx, "s", "turn", started, false); err != nil || turn != "turn" {
		t.Fatalf("dispatch: %q %v", turn, err)
	}
	const days = 5
	reset := int64(days * 24 * 3600)
	ended := contract.Event{ID: "end", Room: "r", Time: now, From: principal, Body: contract.TurnEnded{Session: "s", Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}}
	if err := s.EndTurn(ctx, "s", contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: reset, OneWake: true}, ended); err != nil {
		t.Fatal(err)
	}
	var retryAt int64
	if err := s.Read().QueryRowContext(ctx, "SELECT model_retry_at FROM sessions WHERE id='s'").Scan(&retryAt); err != nil {
		t.Fatal(err)
	}
	if _, due, err := s.CompactionDue(ctx, "s", true); err != nil || due {
		t.Fatalf("an agy session compacts after a failed turn: %v %v", due, err)
	}
	if _, due, err := s.CompactionDue(ctx, "s", false); err != nil || !due {
		t.Fatalf("a pig session stopped compacting: %v %v", due, err)
	}
	resetAt := now.Add(time.Duration(reset) * time.Second).UnixMilli()
	if retryAt != resetAt {
		t.Fatalf("retry at %d, want exactly the reset %d", retryAt, resetAt)
	}
	wakes := func() int {
		var n int
		if err := s.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id='s' AND state='pending' AND wake=1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := s.WakeModelRetries(ctx, now.Add(time.Duration(reset-3600)*time.Second)); err != nil || wakes() != 0 {
		t.Fatalf("woken before the reset: %d %v", wakes(), err)
	}
	if err := s.WakeModelRetries(ctx, now.Add(time.Duration(reset)*time.Second)); err != nil || wakes() != 1 {
		t.Fatalf("not woken once at the reset: %d %v", wakes(), err)
	}
	if err := s.WakeModelRetries(ctx, now.Add(time.Duration(reset+120)*time.Second)); err != nil || wakes() != 1 {
		t.Fatalf("woken twice: %d %v", wakes(), err)
	}
}

// An agy turn that fails on a limit gets at most one wake, at its own reset, however many retries came before; with no stated reset
// it gets none. Other profiles keep the eight-attempt ladder.
func TestAgyQuotaRetryIsOutsideTheLadder(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, c := range []struct {
		name    string
		reset   int64
		oneWake bool
		retries int
		want    string
	}{
		{"agy, reset, after eight attempts", 3600, true, 8, "at the reset"},
		{"agy, a short reset", 7, true, 0, "at 7s"},
		{"agy, no reset", 0, true, 0, "none"},
		{"pig, no reset, ladder", 0, false, 0, "ladder"},
		{"pig, reset, after eight attempts", 3600, false, 8, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at,model_retries) VALUES('s','r','s','p','open','',0,` + string(rune('0'+c.retries)) + `);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
				t.Fatal(err)
			}
			principal := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
			post := contract.Event{ID: "post", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.PostCreated{Text: "work"}}
			if _, err := s.Post(ctx, post, []contract.Delivery{{To: principal, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
				t.Fatal(err)
			}
			started := contract.Event{ID: "start", Room: "r", Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
			if _, _, _, err := s.Dispatch(ctx, "s", "turn", started, false); err != nil {
				t.Fatal(err)
			}
			ended := contract.Event{ID: "end", Room: "r", Time: now, From: principal, Body: contract.TurnEnded{Session: "s", Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed}}
			output := contract.TurnOutputRequest{Turn: "turn", Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, ModelRetryAfterSeconds: c.reset, OneWake: c.oneWake}
			if err := s.EndTurn(ctx, "s", output, ended); err != nil {
				t.Fatal(err)
			}
			var retryAt int64
			if err := s.Read().QueryRowContext(ctx, "SELECT model_retry_at FROM sessions WHERE id='s'").Scan(&retryAt); err != nil {
				t.Fatal(err)
			}
			at := time.UnixMilli(retryAt).Sub(now)
			switch c.want {
			case "at the reset":
				if at != time.Hour {
					t.Fatalf("retry in %v, want exactly the reset", at)
				}
			case "at 7s":
				if at != 7*time.Second {
					t.Fatalf("retry in %v, want exactly 7s, with no floor", at)
				}
			case "ladder":
				if at < 5*time.Minute {
					t.Fatalf("retry in %v, want the first ladder step", at)
				}
			case "none":
				if retryAt > 0 {
					t.Fatalf("a retry was scheduled in %v", at)
				}
			}
		})
	}
}
