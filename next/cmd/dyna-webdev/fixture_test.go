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

func TestRunFixtures(t *testing.T) {
	audit := auditSteps("ses_1")
	phases, failed := map[string]int{}, 0
	for _, step := range audit {
		phases[step.phase]++
		if step.reason != "" {
			failed++
		}
	}
	if len(audit) != 149 || phases["Review pages"] != 120 || failed < 3 {
		t.Fatalf("audit %d steps, phases %v, %d failed", len(audit), phases, failed)
	}
	running, waiting := 0, 0
	for _, step := range triageSteps() {
		if step.took == 0 {
			running++
		}
		if step.waiting {
			waiting++
		}
	}
	if running != 3 || waiting != 1 {
		t.Fatalf("triage has %d running, %d waiting", running, waiting)
	}
}
