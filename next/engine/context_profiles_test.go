package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestContextProfilesAtDepthLimit(t *testing.T) {
	r := newGrantRig(t)
	r.s.SetMaxDepth(2)
	for _, who := range []contract.Principal{r.root, r.worker} {
		res, problem := r.s.ContextSnapshot(t.Context(), who, contract.ContextSnapshotRequest{Section: contract.ContextProfiles})
		if problem != nil {
			t.Fatal(problem)
		}
		var text string
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextProfiles {
				text = sec.Text
			}
		}
		atLimit := strings.Contains(text, "depth limit")
		if (who == r.worker) != atLimit {
			t.Fatalf("depth limit line for %v: %q", who, text)
		}
	}
}
