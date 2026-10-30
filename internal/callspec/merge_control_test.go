package callspec

import (
	"testing"
	"time"
)

func TestMergeControlDeadlineAllowsPublicationWaitAndRetry(t *testing.T) {
	if got := Deadline("MergeControl"); got < 11*time.Minute {
		t.Fatalf("merge control deadline %s cannot cover the ten-minute lock wait and one-minute retry", got)
	}
}
