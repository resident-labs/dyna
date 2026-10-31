package eval

import "testing"

func TestGateRequiresCompleteMatchedRunsAndCountsAllTokens(t *testing.T) {
	cli, mcp := make([]Run, 20), make([]Run, 20)
	for i := range cli {
		cli[i] = Run{Task: i + 1, Measured: true, Correct: true, Usage: Usage{Input: 50, CacheRead: 20, CacheWrite: 20, Output: 10}}
		mcp[i] = Run{Task: i + 1, Measured: true, Correct: true, Usage: Usage{Input: 40, CacheRead: 20, CacheWrite: 10, Output: 10}}
	}
	if got := Compare(cli, mcp); !got.Measured || !got.Pass {
		t.Fatalf("threshold should pass: %+v", got)
	}
	mcp[0].Usage.Output++
	if got := Compare(cli, mcp); !got.Measured || got.Pass {
		t.Fatalf("all token categories count: %+v", got)
	}
	mcp[0].Usage.Output--
	mcp[0].Correct = false
	if got := Compare(cli, mcp); got.Pass {
		t.Fatalf("lower accuracy passed: %+v", got)
	}
	mcp[0].Task = 2
	if got := Compare(cli, mcp); got.Measured {
		t.Fatalf("duplicate task measured: %+v", got)
	}
}
