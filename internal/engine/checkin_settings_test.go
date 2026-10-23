package engine

import (
	"strings"
	"testing"

	"dyna-agent/internal/settings"
)

// The check-in interval moved to the hook, so the old setting refuses a write
// and names the hook that owns it, instead of taking a value that no pass reads.
func TestCheckInIntervalSettingNamesTheHook(t *testing.T) {
	s := services{}
	_, err := s.setSetting(t.Context(), settings.KeySessionCheckInInterval, "5m")
	if err == nil || !strings.Contains(err.Error(), "parent-checkins") || !strings.Contains(err.Error(), "Settings > Hooks") {
		t.Fatalf("setting error = %v", err)
	}
}
