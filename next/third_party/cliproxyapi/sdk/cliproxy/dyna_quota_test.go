package cliproxy

import "testing"

func TestDynaQuotaSignalsAreTypedAndBounded(t *testing.T) {
	got := dynaQuotaWindow(map[string]string{
		"X-Codex-Primary-Used-Percent":        "NaN",
		"X-Codex-Primary-Window-Minutes":      "60",
		"X-Codex-Primary-Reset-At":            "-1",
		"X-Codex-Primary-Reset-After-Seconds": "0",
	}, "X-Codex-Primary-")
	if got.UsedPercent != nil || got.ResetAt != nil || got.WindowMinutes == nil || *got.WindowMinutes != 60 || got.ResetAfterSeconds == nil || *got.ResetAfterSeconds != 0 {
		t.Fatal("invalid values passed or optional zero was lost")
	}
}
