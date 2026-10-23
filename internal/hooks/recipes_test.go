package hooks

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/shellquote"
)

func TestDoctorRecipeInstallsScheduleWatchdog(t *testing.T) {
	_, repo := setup(t)
	h, err := DoctorRecipe(context.Background(), repo, "/fake/dyna", "doctor")
	if err != nil {
		t.Fatal(err)
	}
	if h.Label != doctorLabel {
		t.Errorf("label = %q, want %q", h.Label, doctorLabel)
	}
	if h.Event != Schedule || h.EveryMinutes != 1 {
		t.Errorf("event = %q every %d minutes, want schedule every 1 minute", h.Event, h.EveryMinutes)
	}
	want := shellquote.Quote("/fake/dyna") + " doctor examine machine --if " + shellquote.Quote(DoctorCondition)
	if h.Command != want {
		t.Errorf("command = %q, want %q", h.Command, want)
	}
	if !strings.Contains(h.Command, "pressure.memory>20%") {
		t.Errorf("command %q lost part of the condition", h.Command)
	}
	if h.Cwd != "main" || h.Timeout != "1m" || h.OnFail != "alert" {
		t.Errorf("cwd %q timeout %q onFail %q, want main, 1m, alert", h.Cwd, h.Timeout, h.OnFail)
	}
	if err := Validate(h); err != nil {
		t.Fatalf("recipe hook is invalid: %v", err)
	}
}
