package websearch

import (
	"testing"
	"time"
)

func TestAttemptBudgetAndResultLimit(t *testing.T) {
	if got := attemptBudget(time.Minute); got != 30*time.Second {
		t.Fatalf("long budget: %v", got)
	}
	if got := attemptBudget(4 * time.Second); got != 4*time.Second {
		t.Fatalf("short budget: %v", got)
	}
	if (Query{}).results() != 5 || (Query{Limit: 100}).results() != 10 {
		t.Fatal("wrong result limits")
	}
}
