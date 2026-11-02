package engine

import (
	"context"
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
	channel, problem := b.s.BoxChannel(ctx, b.caller, "")
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

func TestARestartedEngineDoesNotRepeatARecordedCompaction(t *testing.T) {
	b := newQuietBox(t)
	ask := b.ask(t)
	b.answer(t, contract.BoxCompactedRequest{Turn: ask.Turn, Compacted: true})
	if turn, due, err := b.s.store.CompactionDue(b.ctx, b.id); err != nil || due || turn != ask.Turn {
		t.Fatalf("due %s %v: %v", turn, due, err)
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
