package guidance

import (
	"strings"
	"testing"
)

func TestRootGuidanceKeepsCallAndMergeInstructions(t *testing.T) {
	const merge = "When a merge batch fails, Dyna pauses the queue and wakes root once. Take control with `dyna git merge status` (`dyna guide git`): return a PR, retry, bisect, or publish a successful trial, then resume. Successful batches stay automatic; review and CI feedback still goes to authors."
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
