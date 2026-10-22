package subagent

import (
	"context"
	"sync"
	"testing"
)

// enqueueDoneContext delivers the pending claim reply when code asks for the
// budget channel, so only a join that waits on the budget sees it.
type enqueueDoneContext struct {
	context.Context
	done  <-chan struct{}
	reply chan inputReply
	once  sync.Once
}

func (c *enqueueDoneContext) Done() <-chan struct{} {
	c.once.Do(func() {
		c.reply <- inputReply{input: ClaimedInput{Cursor: 150}}
	})
	return c.done
}

// A claim reply the harness never consumed stays with the next turn, which
// recovers the post. Deactivate leaves it alone and closes at the read cursor.
func TestDeactivateKeepsAnUnconsumedClaimForTheNextTurn(t *testing.T) {
	s, id := turnStore(t)
	done := make(chan inputReply, 1)
	done <- inputReply{input: ClaimedInput{Cursor: 150}}
	g := &turnGate{store: s, meta: Meta{ID: id}, turn: 1, cursor: 100, ctx: context.Background()}
	g.pending = &pendingInput{done: done, final: true}
	if err := g.Activate(); err != nil {
		t.Fatal(err)
	}
	g.Deactivate()
	if g.cursor != 100 {
		t.Fatalf("gate cursor = %d, want the read cursor 100", g.cursor)
	}
	select {
	case <-g.pending.done:
		// The reply is still there for the next turn's recovery.
	default:
		t.Fatal("Deactivate consumed the pending reply")
	}
	if at, ok := s.closedAt(id, 1); !ok || at != 100 {
		t.Fatalf("closedAt = %d, %v; want 100", at, ok)
	}
}

// A canceled turn still joins an outstanding claim through the terminal
// budget, and the boundary covers its committed cursor. The reply is released
// only when the join asks the budget, so the cancellation itself settles
// nothing. The gate's own cursor stays put for the next turn's recovery.
func TestSettleCoversACancelledTurnsCommittedClaim(t *testing.T) {
	s, id := turnStore(t)
	reply := make(chan inputReply, 1)
	budget := &enqueueDoneContext{Context: context.Background(), done: make(chan struct{}), reply: reply}
	canceled, cancelTurn := context.WithCancel(context.Background())
	cancelTurn()
	g := &turnGate{store: s, meta: Meta{ID: id}, turn: 1, cursor: 100, ctx: canceled}
	g.pending = &pendingInput{done: reply, final: true}
	if err := g.Activate(); err != nil {
		t.Fatal(err)
	}
	if got := g.Settle(budget); got != 150 {
		t.Fatalf("Settle = %d, want the committed claim cursor 150", got)
	}
	if g.cursor != 100 {
		t.Fatalf("gate cursor = %d, want the read cursor 100", g.cursor)
	}
	g.Deactivate()
	if at, ok := s.closedAt(id, 1); !ok || at != 100 {
		t.Fatalf("closedAt = %d, %v; want 100", at, ok)
	}
}

// An expired budget takes a claim reply that already arrived and keeps the
// read cursor when none arrived.
func TestSettleKeepsTheCursorWhenTheBudgetExpired(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply *inputReply
		want  int64
	}{
		{"ready", &inputReply{input: ClaimedInput{Cursor: 150}}, 150},
		{"missing", nil, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := turnStore(t)
			budget, expire := context.WithCancel(context.Background())
			expire() // expire on cue, not on a clock
			done := make(chan inputReply, 1)
			if tc.reply != nil {
				done <- *tc.reply
			}
			g := &turnGate{store: s, meta: Meta{ID: id}, turn: 1, cursor: 100, ctx: context.Background()}
			g.pending = &pendingInput{done: done, final: true}
			if got := g.Settle(budget); got != tc.want {
				t.Fatalf("Settle = %d, want %d", got, tc.want)
			}
			if g.cursor != 100 {
				t.Fatalf("gate cursor = %d, want 100", g.cursor)
			}
		})
	}
}
