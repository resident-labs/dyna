package eval

import (
	"strings"
	"testing"
)

func TestToolArrayComparisonDoesNotCrossCompactions(t *testing.T) {
	g0, g1 := 0, 1
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	records := []HashRecord{
		{Session: "s", Provider: Opus, Ordinal: 1, ToolsSHA256: a, Generation: &g0},
		{Session: "s", Provider: Opus, Ordinal: 2, ToolsSHA256: a, Generation: &g0},
		{Session: "s", Provider: Opus, Ordinal: 3, ToolsSHA256: b, Generation: &g1},
		{Session: "s", Provider: Opus, Ordinal: 4, ToolsSHA256: a, Generation: &g1},
	}
	if got := CompareToolArrays(records); got.Compared != 2 || got.Mismatches != 1 || got.Unmeasured {
		t.Fatalf("unexpected result: %+v", got)
	}
	records[2].Generation = nil
	if got := CompareToolArrays(records); !got.Unmeasured {
		t.Fatalf("unknown join measured: %+v", got)
	}
}
