package voice

import (
	"strings"
	"testing"
)

func TestReviewBudgetDoesNotTruncateBelowLimit(t *testing.T) {
	var b outputBudget
	s := strings.Repeat("x", 3000)
	got := b.append(s) + b.finish()
	if got != s {
		t.Fatalf("3000-byte output was truncated: got %d bytes with marker=%v", len(got), strings.Contains(got, truncationMarker))
	}
}
