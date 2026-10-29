package transcript

import (
	"testing"

	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

// A stored first call reaches the turn view, and a turn without one stays nil.
func TestSessionTurnKeepsTheFirstCall(t *testing.T) {
	first := &workeroutput.CallUsage{}
	if got := SessionTurn(subagent.Turn{N: 1, FirstCall: first}, nil, false); got.FirstCall != first {
		t.Fatalf("FirstCall = %+v, want the stored first call", got.FirstCall)
	}
	if got := SessionTurn(subagent.Turn{N: 2}, nil, false); got.FirstCall != nil {
		t.Fatalf("FirstCall = %+v, want unknown nil", got.FirstCall)
	}
}
