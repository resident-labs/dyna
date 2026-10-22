package native

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The provider names its buckets after what is left ("Weekly Limit
// Remaining") while the window stores used percent, so an untouched account
// printed "Weekly Limit Remaining 0%" and read as exhausted. The label must
// match the value it is shown with.
func TestSavedAntigravityLabelsMatchTheUsedPercentValue(t *testing.T) {
	c := directTestAgy(t, time.Now().Add(time.Hour))
	m := agyTestManager(t)
	calls := 0
	m.client = directTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return directTestResponse(200, `{"cloudaicompanionProject":"observed-project","currentTier":{"id":"free-tier"},"paidTier":{"id":"g1-pro-tier","name":"Google AI Pro"}}`), nil
		}
		_, _ = io.ReadAll(r.Body)
		return directTestResponse(200, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","displayName":"Session Limit Remaining","window":"5h","remainingFraction":1,"resetTime":"2026-10-17T16:00:00Z"},{"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","window":"weekly","remainingFraction":1}]}]}`), nil
	})
	o, err := m.ProbeSaved(context.Background(), c)
	if err != nil || calls != 2 || len(o.Windows) != 2 {
		t.Fatalf("unexpected Antigravity observation: %+v, %v", o, err)
	}
	for i, want := range []string{
		"Gemini Models: Session Limit (5h)",
		"Gemini Models: Weekly Limit (weekly)",
	} {
		w := o.Windows[i]
		if w.Label != want {
			t.Fatalf("window %d label = %q, want %q", i, w.Label, want)
		}
		if w.UsedPercent == nil || *w.UsedPercent != 0 {
			t.Fatalf("window %d used = %#v, want 0%% for an untouched account", i, w.UsedPercent)
		}
		if strings.Contains(w.Label, "Remaining") {
			t.Fatalf("window %d label %q still reads as a remaining value", i, w.Label)
		}
	}
}
