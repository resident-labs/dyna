package main

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestLongFixture(t *testing.T) {
	turns := fixtureTurns()
	tools, posts, steers, failed, running := 0, 0, 0, 0, 0
	longFinal := false
	for _, turn := range turns {
		if turn.Ended == nil {
			running++
		}
		if turn.Outcome == contract.TurnFailed {
			failed++
		}
		for _, item := range turn.Items {
			if item.Kind == contract.TranscriptTool {
				tools++
			}
			if item.Kind == contract.TranscriptSteer {
				steers++
			}
			posts += len(item.Posts)
			longFinal = longFinal || strings.Contains(item.Text, "### Verification")
		}
	}
	if len(turns) < 12 || tools < 40 || posts < 3 || steers != 2 || failed != 1 || running != 1 || !longFinal {
		t.Fatalf("turns=%d tools=%d posts=%d steers=%d failed=%d running=%d long final=%v", len(turns), tools, posts, steers, failed, running, longFinal)
	}
}
