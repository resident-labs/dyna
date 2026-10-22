package guidance

import (
	"strings"
	"testing"
)

func TestRootGuidanceKeepsCallAndMergeInstructions(t *testing.T) {
	const merge = "When a merge batch fails, Dyna pauses the queue and sends root one waking notice. Take control with `dyna git merge status`. Read `dyna guide git`, then return a PR, retry the batch, run bisect on demand, or publish a successful trial. Resume when the queue can continue. Successful batches remain automatic. Per-PR review and CI feedback still goes to authors."
	for _, internal := range []bool{false, true} {
		text := RootBlock(internal)
		if !strings.Contains(text, "To talk with the owner, run `dyna call --reason") {
			t.Fatalf("internal=%v: missing call guidance", internal)
		}
		if !strings.Contains(text, "\n\n"+merge+"\n") {
			t.Fatalf("internal=%v: merge guidance was removed or changed", internal)
		}
		if strings.Contains(text, "a post to `@voice-call` rings the owner") {
			t.Fatalf("internal=%v: old implicit ringing remains", internal)
		}
	}
}
