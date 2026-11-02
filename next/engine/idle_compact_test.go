package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// quietBox is a quiet session with its box channel open, one reconciler pass from a stop.
type quietBox struct {
	s       *Service
	host    *dormantHost
	room    contract.RoomID
	id      contract.SessionID
	ctx     context.Context
	caller  contract.Principal
	channel <-chan contract.BoxMessage
}

func newQuietBox(t *testing.T) *quietBox {
	t.Helper()
	s, host, room, id, ctx := quietSession(t, 0)
	b := &quietBox{s: s, host: host, room: room, id: id, ctx: ctx, caller: contract.Principal{Kind: contract.PrincipalSession, Session: id}}
	b.connect(t)
	return b
}

func (b *quietBox) connect(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(b.ctx)
	channel, problem := b.s.BoxChannel(ctx, b.caller)
	if problem != nil {
		t.Fatal(problem)
	}
	t.Cleanup(func() {
		cancel()
		for range channel {
		}
	})
	b.channel = channel
}

// next reads the box channel for one message of the wanted type, skipping others.
func next[T contract.BoxMessageBody](t *testing.T, b *quietBox) (T, bool) {
	t.Helper()
	timeout := time.After(200 * time.Millisecond)
	for {
		select {
		case message := <-b.channel:
			if body, ok := message.Body.(T); ok {
				return body, true
			}
		case <-timeout:
			var none T
			return none, false
		}
	}
}

func (b *quietBox) pass(t *testing.T) {
	t.Helper()
	passLifecycle(t, b.s, b.ctx)
}

func (b *quietBox) ask(t *testing.T) contract.Compact {
	t.Helper()
	b.pass(t)
	ask, ok := next[contract.Compact](t, b)
	if !ok {
		t.Fatal("the box was not asked to compact")
	}
	if ask.OverTokens != 100000 || ask.Turn != "turn_quiet" {
		t.Fatalf("ask %+v", ask)
	}
	if b.host.stops != 0 {
		t.Fatal("the box stopped before the answer")
	}
	return ask
}

func (b *quietBox) answer(t *testing.T, req contract.BoxCompactedRequest) {
	t.Helper()
	if _, problem := b.s.BoxCompacted(b.ctx, b.caller, req); problem != nil {
		t.Fatal(problem)
	}
}

func (b *quietBox) compactions(t *testing.T) []contract.SessionCompacted {
	t.Helper()
	events, err := b.s.store.Events(b.ctx, b.room, 0, 256, []contract.EventType{"session.compacted"})
	if err != nil {
		t.Fatal(err)
	}
	var out []contract.SessionCompacted
	for _, event := range events {
		out = append(out, event.Body.(contract.SessionCompacted))
	}
	return out
}

func TestACompactionRunsOnceBeforeAQuietStop(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	b.pass(t)
	if _, again := next[contract.Compact](t, b); again || b.host.stops != 0 {
		t.Fatalf("the wait asked again, or stopped: %d stops", b.host.stops)
	}
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn, Compacted: true, TokensBefore: 150000, TokensAfter: 9000, Usage: contract.Usage{InputTokens: 140000, OutputTokens: 800}})
	b.pass(t)
	if b.host.stops != 1 || stateOf(t, b.s, b.ctx, b.id) != contract.BoxStopped {
		t.Fatal("the box did not stop after its compaction")
	}
	recorded := b.compactions(t)
	if len(recorded) != 1 || recorded[0].Turn != ask.Turn || recorded[0].Session != b.id || recorded[0].TokensBefore != 150000 || recorded[0].TokensAfter != 9000 || recorded[0].Usage.InputTokens != 140000 || recorded[0].Usage.OutputTokens != 800 {
		t.Fatalf("recorded %+v", recorded)
	}
}

func TestASmallContextStopsWithNothingRecorded(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn})
	b.pass(t)
	if b.host.stops != 1 || len(b.compactions(t)) != 0 {
		t.Fatalf("%d stops, recorded %+v", b.host.stops, b.compactions(t))
	}
}

func TestAFailedCompactionStopsAndIsNotTriedAgainThisQuietPeriod(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn, Error: "the model is down"})
	b.pass(t)
	if b.host.stops != 1 {
		t.Fatal("a failed compaction blocked the stop")
	}
	recorded := b.compactions(t)
	if len(recorded) != 1 || recorded[0].Error != "the model is down" {
		t.Fatalf("recorded %+v", recorded)
	}
	// A terminal brings the box back with no turn, so the quiet period goes on.
	if err := b.s.store.Write(b.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(b.ctx, "UPDATE boxes SET state='ready',want='run' WHERE session=?", b.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.pass(t)
	if _, again := next[contract.Compact](t, b); again || b.host.stops != 2 {
		t.Fatalf("asked again, or held the stop: %d stops", b.host.stops)
	}
}

func TestARestartedEngineDoesNotRepeatARecordedCompaction(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn, Compacted: true})
	if turn, due, err := b.s.store.CompactionDue(b.ctx, b.id); err != nil || due || turn != ask.Turn {
		t.Fatalf("due %s %v: %v", turn, due, err)
	}
}

func TestAnUnansweredAskRunsOutAndTheBoxStops(t *testing.T) {
	b := newQuietBox(t)
	b.ask(t)
	at := b.s.now().Add(idleCompactWait)
	b.s.now = func() time.Time { return at }
	b.pass(t)
	if b.host.stops != 1 {
		t.Fatal("an unanswered ask blocked the stop")
	}
}

func TestAPausedRoomStopsWithoutCompacting(t *testing.T) {
	b := newQuietBox(t)
	if err := b.s.store.Write(b.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(b.ctx, "UPDATE rooms SET paused_at=1 WHERE id=?", b.room)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	b.pass(t)
	if _, asked := next[contract.Compact](t, b); asked || b.host.stops != 1 {
		t.Fatalf("asked %v, %d stops", asked, b.host.stops)
	}
}

func TestADisconnectedBoxStopsWithoutCompacting(t *testing.T) {
	s, host, _, _, ctx := quietSession(t, 0)
	passLifecycle(t, s, ctx)
	if host.stops != 1 {
		t.Fatal("the stop waited for a box that is not there")
	}
}

func TestAWakeDuringACompactionRunsItsTurnAndLosesNothing(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	wakeSession(t, b.s, b.ctx, b.room, b.id)
	start, ok := next[contract.TurnStart](t, b)
	if !ok || len(start.Inputs) != 1 {
		t.Fatalf("the wake dispatched %+v", start)
	}
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn, Compacted: true})
	b.pass(t)
	if b.host.stops != 0 || stateOf(t, b.s, b.ctx, b.id) != contract.BoxReady {
		t.Fatalf("the box stopped under its turn: %d stops", b.host.stops)
	}
	if _, problem := b.s.TurnOutput(b.ctx, b.caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "ok"}); problem != nil {
		t.Fatal(problem)
	}
	// The compaction belonged to the quiet period before that turn, so the next one may compact again.
	at := b.s.now().Add(11 * time.Minute)
	b.s.now = func() time.Time { return at }
	b.pass(t)
	if again, ok := next[contract.Compact](t, b); !ok || again.Turn != start.Turn {
		t.Fatalf("the next quiet period asked %+v", again)
	}
}
