package ratelimit

import (
	"testing"
	"time"
)

func TestAllowCountsPerKeyAndWindow(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	l := New(func() time.Time { return now })
	steps := []struct {
		name    string
		key     string
		max     int
		per     time.Duration
		advance time.Duration
		want    bool
	}{
		{"first request", "a", 2, time.Minute, 0, true},
		{"second request", "a", 2, time.Minute, 0, true},
		{"over the limit", "a", 2, time.Minute, 0, false},
		{"another key counts alone", "b", 2, time.Minute, 0, true},
		{"a longer window keeps its count", "c", 1, time.Hour, 0, true},
		{"the longer window still limits", "c", 1, time.Hour, time.Minute, false},
		{"the minute window rolls over", "a", 2, time.Minute, 0, true},
		{"the new window counts again", "a", 2, time.Minute, 0, true},
		{"the new window limits", "a", 2, time.Minute, 0, false},
	}
	for _, step := range steps {
		now = now.Add(step.advance)
		if got := l.Allow(step.key, step.max, step.per); got != step.want {
			t.Fatalf("%s: Allow(%q, %d, %s) = %v, want %v", step.name, step.key, step.max, step.per, got, step.want)
		}
	}
}
