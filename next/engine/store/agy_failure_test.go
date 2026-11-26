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
