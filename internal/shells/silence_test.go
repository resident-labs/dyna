package shells

import (
	"dyna-agent/internal/activity"
	"testing"
	"time"
)

func TestQuietPeriodRequiresContinuousFreshCoverage(t *testing.T) {
	now := time.Unix(3600, 0)
	threshold := 30 * time.Minute
	signal := activity.Signal{Copier: activity.CopierRunning, CPU: activity.CPUUnsupported, ObservedSince: now.Add(-time.Hour), LastOutput: now.Add(-threshold), LeaseUntil: now.Add(time.Second)}
	if !quietPeriod(signal, now, threshold) {
		t.Fatal("continuous quiet observation should be reported")
	}
	cases := []activity.Signal{signal, signal, signal, signal}
	cases[0].LeaseUntil = now
	cases[1].Copier = activity.CopierUnknown
	cases[2].ObservedSince = now.Add(-time.Minute)
	cases[3].LastOutput = now.Add(-time.Minute)
	for _, s := range cases {
		if quietPeriod(s, now, threshold) {
			t.Fatalf("unproven quiet period: %+v", s)
		}
	}
	if quietPeriod(signal, now, 0) {
		t.Fatal("disabled threshold reported")
	}
}
