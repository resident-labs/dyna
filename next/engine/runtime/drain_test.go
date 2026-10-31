package runtime

import (
	"testing"
	"time"
)

func TestDrainDeadline(t *testing.T) {
	done := make(chan struct{})
	deadline := make(chan time.Time, 1)
	deadline <- time.Time{}
	waitForDrain(done, deadline)
}
