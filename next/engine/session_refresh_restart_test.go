package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// refreshedNotices reads the refresh restart notices stored for a session, each with whether its delivery wakes.
func refreshedNotices(t *testing.T, s *Service, ctx context.Context, sid contract.SessionID) (wakes []bool, openGoals []bool) {
	t.Helper()
	rows, err := s.store.Read().QueryContext(ctx, `SELECT d.wake, coalesce(json_extract(CAST(e.body AS TEXT),'$.openGoal'),0) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='session.restarted' AND json_extract(CAST(e.body AS TEXT),'$.refreshed')=1 ORDER BY d.seq`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var wake, goal bool
		if err := rows.Scan(&wake, &goal); err != nil {
			t.Fatal(err)
		}
		wakes, openGoals = append(wakes, wake), append(openGoals, goal)
	}
	return wakes, openGoals
}

// StopStale leaves a ready snapshot with want=stop until the lifecycle worker
// stops the old binary. Refresh cannot mark it, but its persisted ready -> stopped
// transition supplies the deploy smoke's unmarked-target evidence without a wake.
func TestRefreshDuringStaleStopObservesUpdate(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	_, disconnect := connectBox(t, s, ctx, sid, oldBinary)
	waitWant(t, s, ctx, sid, "stop")
	page, problem := s.SessionList(ctx, refreshOwner, contract.SessionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	var box contract.BoxID
	for _, session := range page.Sessions {
		if session.ID == sid && session.BoxState == contract.BoxReady && session.Turn == "" && session.Computer == contract.ComputerStopped {
			box = session.Box
		}
	}
	if box == "" {
		t.Fatal("stale-stop window not visible as an idle ready session")
	}
	response, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Session: sid})
	if problem != nil || len(response.Marked) != 0 {
		t.Fatalf("marked=%v: %v", response.Marked, problem)
	}
	passLifecycle(t, s, ctx)
	disconnect()
	if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("stale box did not stop")
	}
	passLifecycle(t, s, ctx)
	if host.starts != 0 || boxWant(t, s, ctx, sid) != "stop" {
		t.Fatal("idle stale-stopped box should remain asleep without a wake")
	}
	var transitions int
	if err := s.store.Read().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE room=? AND seq>? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.session')=? AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='stopped' AND json_extract(CAST(body AS TEXT),'$.from')='ready'`, room, page.Seq, sid, box).Scan(&transitions); err != nil {
		t.Fatal(err)
	}
	if transitions != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("stop transitions=%d state=%s", transitions, stateOf(t, s, ctx, sid))
	}
}

// A refresh while a turn runs stops the box once the turn ends, and the engine starts it again by itself, whoever asked: one
// notice for that start, which wakes the session whether or not it holds a goal, and says to continue only with an open goal.
func TestRefreshRestartsTheBoxAfterItsTurn(t *testing.T) {
	for _, byOwner := range []bool{true, false} {
		for _, goal := range []bool{true, false} {
			t.Run(fmt.Sprintf("owner=%v goal=%v", byOwner, goal), func(t *testing.T) {
				s, host, room, sid, ctx := staleSession(t)
				// The session was started with a goal; without one, it has ended.
				if !goal {
					if err := s.store.Write(ctx, func(tx *sql.Tx) error {
						_, err := tx.Exec(`UPDATE goals SET state='done' WHERE session=?`, sid)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				turn := openTurn(t, s, ctx, room, sid, "turn_open")
				turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
				turnInputs(t, turns)
				caller := boxCaller(sid)
				if byOwner {
					caller = refreshOwner
				}
				response, problem := s.SessionRefresh(ctx, caller, contract.SessionRefreshRequest{Session: sid})
				if problem != nil || len(response.Marked) != 1 {
					t.Fatalf("marked=%v: %v", response.Marked, problem)
				}
				if host.stops != 0 {
					t.Fatalf("box stopped during its turn")
				}
				endTurn(t, s, ctx, sid, turn)
				waitWant(t, s, ctx, sid, "stop")
				passLifecycle(t, s, ctx)
				disconnect()
				if host.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
					t.Fatalf("stops=%d state=%s", host.stops, stateOf(t, s, ctx, sid))
				}
				passLifecycle(t, s, ctx)
				if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
					t.Fatalf("starts=%d state=%s, want the engine to start the box again", host.starts, stateOf(t, s, ctx, sid))
				}
				wakes, openGoals := refreshedNotices(t, s, ctx, sid)
				if len(wakes) != 1 || !wakes[0] || len(openGoals) != 1 || openGoals[0] != goal {
					t.Fatalf("wakes=%v open goals=%v, want one waking notice", wakes, openGoals)
				}
				passLifecycle(t, s, ctx)
				if wakes, _ := refreshedNotices(t, s, ctx, sid); len(wakes) != 1 {
					t.Fatalf("%d notices after a further pass", len(wakes))
				}
			})
		}
	}
}

// A refresh of a box that is already stopped leaves it marked, so its next start, by an input, is on the new image and says
// nothing: the input is the news. A start that fails stops it again
// with the mark still on, and that tells nothing either.
func TestRefreshOfAStoppedBoxMarksIt(t *testing.T) {
	s, host, room, sid, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	response, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Session: sid})
	if problem != nil || len(response.Marked) != 1 {
		t.Fatalf("marked=%v: %v", response.Marked, problem)
	}
	if host.starts != 0 {
		t.Fatal("a refresh started a stopped box")
	}
	if handle, ok := s.live.LookupSession(sid); !ok || handle.Refresh() == "" {
		t.Fatal("the stopped box was not left marked")
	}
	host.startErr = errors.New("no start")
	wakeSession(t, s, ctx, room, sid)
	passLifecycle(t, s, ctx)
	if host.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatalf("starts=%d state=%s", host.starts, stateOf(t, s, ctx, sid))
	}
	if wakes, _ := refreshedNotices(t, s, ctx, sid); len(wakes) != 0 {
		t.Fatalf("a box woken by an input told its session: %v", wakes)
	}
}

// A start that fails after a refresh stop retries by the usual rules and tells nothing more: the notice keys once per mark.
func TestRefreshRestartFailingStartNoticesOnce(t *testing.T) {
	s, host, room, sid, ctx := staleSession(t)
	host.startErr = errors.New("no start")
	turn := openTurn(t, s, ctx, room, sid, "turn_open")
	turns, disconnect := connectBox(t, s, ctx, sid, newBinary)
	turnInputs(t, turns)
	if _, problem := s.SessionRefresh(ctx, refreshOwner, contract.SessionRefreshRequest{Session: sid}); problem != nil {
		t.Fatal(problem)
	}
	endTurn(t, s, ctx, sid, turn)
	waitWant(t, s, ctx, sid, "stop")
	passLifecycle(t, s, ctx)
	disconnect()
	now := s.now()
	for range 3 {
		passLifecycle(t, s, ctx)
		now = now.Add(time.Hour)
		s.now = func() time.Time { return now }
	}
	if host.starts < 2 {
		t.Fatalf("starts=%d, want retries", host.starts)
	}
	if wakes, _ := refreshedNotices(t, s, ctx, sid); len(wakes) != 1 {
		t.Fatalf("notices=%v after failing starts, want one", wakes)
	}
}
