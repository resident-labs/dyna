package view

import "testing"

func TestCheckResultShowsActionRequiredAsWaiting(t *testing.T) {
	if got := checkResult("COMPLETED", "ACTION_REQUIRED"); got != "pending" {
		t.Fatalf("checkResult(COMPLETED, ACTION_REQUIRED) = %q, want pending", got)
	}
}
