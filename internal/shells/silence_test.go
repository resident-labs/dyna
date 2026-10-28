package shells

import (
	"testing"
	"time"
)

func TestSilentLogSkipsCappedAndRecentOutput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
	if !silentLog(now, now.Add(-30*time.Minute), 0, 30*time.Minute) {
		t.Fatal("quiet log should be reported")
	}
	if silentLog(now, now.Add(-time.Minute), 0, 30*time.Minute) || silentLog(now, now.Add(-time.Hour), MaxOutputBytes, 30*time.Minute) {
		t.Fatal("recent or capped log should not be reported")
	}
}
