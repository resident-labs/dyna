package eval

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportKeepsSkippedAndMissingDistinct(t *testing.T) {
	var out bytes.Buffer
	r := CellResult{Cell: Cell{Task: 1, Provider: Claude, Arm: Caller}, Scored: true, Measured: true, Score: Score{Correct: true}, Attempts: []Attempt{{Usage: Usage{Input: 5, CacheRead: 5, Output: 2}}}}
	if err := WriteReport(&out, []CellResult{r}, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| 1 | pass | 12 | 50.0% |", "| 2 | not run |", "| 3 | unmeasured |", "| 17 | not run |", "Accuracy: 1/1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q", want)
		}
	}
	if err := WriteReport(&out, []CellResult{r, r}, true); err == nil {
		t.Fatal("duplicate accepted")
	}
}
