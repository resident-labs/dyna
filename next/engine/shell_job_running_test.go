package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const statusJob = contract.ShellJobID("job_7f3b")

func jobCaller(sid contract.SessionID) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: sid}
}

func reportRunning(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID, minutes int) {
	t.Helper()
	quiet := 12
	if _, problem := s.ShellJobRunning(ctx, jobCaller(sid), contract.ShellJobRunningRequest{Job: statusJob, Description: "Build", RanMinutes: minutes, QuietMinutes: &quiet, Tail: "compiling"}); problem != nil {
		t.Fatal(problem)
	}
}

// jobStatusDeliveries lists every delivery of a job status: to whom, whether it wakes and its state.
func jobStatusDeliveries(t *testing.T, s *Service, ctx context.Context) []string {
	t.Helper()
	rows, err := s.store.Read().QueryContext(ctx, "SELECT d.to_id,d.wake,d.state FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='shell.job.running' ORDER BY d.seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var to, state string
		var wake bool
		if err := rows.Scan(&to, &wake, &state); err != nil {
			t.Fatal(err)
		}
		how := "quiet"
		if wake {
			how = "wake"
		}
		found = append(found, to+" "+how+" "+state)
	}
	return found
}

func oneDelivery(t *testing.T, s *Service, ctx context.Context, want string) {
	t.Helper()
	if got := jobStatusDeliveries(t, s, ctx); len(got) != 1 || got[0] != want {
		t.Fatalf("deliveries=%v, want [%s]", got, want)
	}
}

func startTurn(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, sid contract.SessionID) contract.TurnID {
	t.Helper()
	wakeSession(t, s, ctx, room, sid)
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, _, err := s.store.Dispatch(ctx, sid, "turn_status", event, false)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestRunningJobWakesAnIdleSession(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 1)
	reportRunning(t, s, ctx, sid, 30)
	oneDelivery(t, s, ctx, string(sid)+" wake pending")

	channelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	messages, problem := s.BoxChannel(channelCtx, jobCaller(sid), "")
	if problem != nil {
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range messages {
		}
	}()
	for {
		select {
		case message := <-messages:
			start, ok := message.Body.(contract.TurnStart)
			if !ok {
				continue
			}
			want := "Your shell job 'Build' has been running for 30 minutes. Last output 12 minutes ago:\ncompiling"
			if len(start.Inputs) != 1 || !strings.HasSuffix(start.Inputs[0].Text, want) {
				t.Fatalf("turn inputs=%+v", start.Inputs)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("no turn started for an idle session")
		}
	}
}

// A turn open at the mark takes nothing mid-turn and gets no second turn: the next turn reads the status.
func TestRunningJobWaitsForTheNextTurnWhenOneIsOpen(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	turn := startTurn(t, s, ctx, room, sid)
	reportRunning(t, s, ctx, sid, 30)
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")

	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	steered, inputs, _, err := s.store.Dispatch(ctx, sid, "turn_steer", event, false)
	if err != nil || steered != turn || len(inputs) != 0 {
		t.Fatalf("mid-turn: turn=%s inputs=%+v: %v", steered, inputs, err)
	}
	if _, problem := s.TurnOutput(ctx, jobCaller(sid), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, ShellJobs: 1, Jobs: []contract.ShellJobID{statusJob}}); problem != nil {
		t.Fatal(problem)
	}
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")
	event, err = s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if next, _, _, err := s.store.Dispatch(ctx, sid, "turn_next", event, false); err != nil || next != "" {
		t.Fatalf("the status started a turn by itself: %s %v", next, err)
	}
	wakeSession(t, s, ctx, room, sid)
	event, err = s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if _, inputs, _, err := s.store.Dispatch(ctx, sid, "turn_next", event, false); err != nil || len(inputs) != 2 {
		t.Fatalf("next turn inputs=%+v: %v", inputs, err)
	}
}

func TestEndedJobSendsNoStatusAndTakesBackAnUnreadOne(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	turn := startTurn(t, s, ctx, room, sid)
	reportRunning(t, s, ctx, sid, 30)
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")
	if _, problem := s.ShellJobEnded(ctx, jobCaller(sid), contract.ShellJobEndedRequest{Job: statusJob, End: jobExited, Tail: "built"}); problem != nil {
		t.Fatal(problem)
	}
	if got := jobStatusDeliveries(t, s, ctx); len(got) != 0 {
		t.Fatalf("an unread status outlived its job: %v", got)
	}
	// One sent just before the end can land after it.
	reportRunning(t, s, ctx, sid, 60)
	if got := jobStatusDeliveries(t, s, ctx); len(got) != 0 {
		t.Fatalf("an ended job sent a status: %v", got)
	}
	var events int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM events WHERE type='shell.job.running'").Scan(&events); err != nil || events != 1 {
		t.Fatalf("status events=%d: %v", events, err)
	}
	if _, problem := s.TurnOutput(ctx, jobCaller(sid), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
}

// A long turn or a paused room must not pile these up.
func TestNewerStatusReplacesAnUnreadOne(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	startTurn(t, s, ctx, room, sid)
	reportRunning(t, s, ctx, sid, 15)
	reportRunning(t, s, ctx, sid, 30)
	reportRunning(t, s, ctx, sid, 60)
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")
	var minutes int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT json_extract(CAST(e.body AS TEXT),'$.ranMinutes') FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='shell.job.running'").Scan(&minutes); err != nil || minutes != 60 {
		t.Fatalf("kept the status at %d minutes: %v", minutes, err)
	}
}

func TestOtherJobsKeepTheirStatus(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 1)
	startTurn(t, s, ctx, room, sid)
	reportRunning(t, s, ctx, sid, 15)
	if _, problem := s.ShellJobRunning(ctx, jobCaller(sid), contract.ShellJobRunningRequest{Job: "job_other", RanMinutes: 15}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.ShellJobEnded(ctx, jobCaller(sid), contract.ShellJobEndedRequest{Job: "job_other", End: jobExited}); problem != nil {
		t.Fatal(problem)
	}
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")
}

func TestOnlyASessionReportsAJob(t *testing.T) {
	s, _, _, _, ctx := quietSession(t, 1)
	if _, problem := s.ShellJobRunning(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.ShellJobRunningRequest{Job: statusJob}); problem == nil {
		t.Fatal("the owner reported a job")
	}
}

// A job stopped by hand tells the engine nothing, so its unread status can sit on an idle leaf that may never run again.
func TestUnreadStatusHoldsNoCloseAndWakesNoParent(t *testing.T) {
	s, _, room, sid, ctx := quietSession(t, 0)
	turn := startTurn(t, s, ctx, room, sid)
	reportRunning(t, s, ctx, sid, 30)
	if _, problem := s.TurnOutput(ctx, jobCaller(sid), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	oneDelivery(t, s, ctx, string(sid)+" quiet pending")
	if _, problem := s.GoalDone(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.GoalDoneRequest{Room: room, Goal: "g1", Summary: "finished"}); problem != nil {
		t.Fatal(problem)
	}
	s.now = func() time.Time { return time.Unix(1000000, 0) }
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id=? AND seq NOT IN (SELECT seq FROM events WHERE type='shell.job.running')", sid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped',want='stop',updated_at=0 WHERE session=?", sid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", sid).Scan(&state); err != nil || state != "closing" {
		t.Fatalf("session=%s: %v", state, err)
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: sid, Reason: contract.SessionCloseDormant})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.CloseSession(ctx, sid, event); err != nil {
		t.Fatal(err)
	}
	// Close drops the session's own deliveries, so any left here went to the parent.
	if got := jobStatusDeliveries(t, s, ctx); len(got) != 0 {
		t.Fatalf("status was handed on: %v", got)
	}
}
