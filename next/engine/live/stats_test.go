package live

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestAStatsAnswerReachesItsQuestionOnly(t *testing.T) {
	c := NewChannel(func() {}, make(chan struct{}))
	first, firstAnswer := c.AskStats()
	second, secondAnswer := c.AskStats()
	c.AnswerStats(second, contract.BoxReading{MemUsed: 2})
	if got := <-secondAnswer; got.MemUsed != 2 {
		t.Fatalf("answer %+v", got)
	}
	select {
	case got := <-firstAnswer:
		t.Fatalf("the first question got %+v", got)
	default:
	}
	if first == second || <-c.Asks() != first || <-c.Asks() != second {
		t.Fatal("the box did not get both questions in order")
	}
}

func TestALateStatsAnswerIsDropped(t *testing.T) {
	c := NewChannel(func() {}, make(chan struct{}))
	ask, answer := c.AskStats()
	c.ForgetStats(ask)
	c.AnswerStats(ask, contract.BoxReading{MemUsed: 1})
	c.AnswerStats(ask+1, contract.BoxReading{MemUsed: 1})
	select {
	case got := <-answer:
		t.Fatalf("a forgotten question got %+v", got)
	default:
	}
	if len(c.answers) != 0 {
		t.Fatalf("%d questions kept", len(c.answers))
	}
}

// A box that stopped reading fills the queue. A question past that keeps no entry, so nothing grows.
func TestAFullStatsQueueDropsTheQuestion(t *testing.T) {
	c := NewChannel(func() {}, make(chan struct{}))
	for range cap(c.asks) + 3 {
		c.AskStats()
	}
	if len(c.answers) != cap(c.asks) {
		t.Fatalf("%d questions kept", len(c.answers))
	}
}
