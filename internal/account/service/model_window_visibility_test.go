package service

import (
	"testing"

	"dyna-agent/internal/account/store"
)

func TestHideUnusedModelWindows(t *testing.T) {
	accounts := []Account{{Windows: []Window{
		{ID: "seven_day", Label: "Weekly (7-day)", UsedPercent: pct(45)},
		{ID: store.ModelWindowPrefix + "fable", Label: "Weekly · Fable", UsedPercent: pct(0)},
		{ID: store.ModelWindowPrefix + "opus", Label: "Weekly · Opus", UsedPercent: pct(3)},
	}}}
	hideUnusedModelWindows(accounts)
	got := accounts[0].Windows
	if len(got) != 2 || got[0].ID != "seven_day" || got[1].ID != store.ModelWindowPrefix+"opus" {
		t.Fatalf("hideUnusedModelWindows kept %+v, want the account window and the used Opus window", got)
	}
}
