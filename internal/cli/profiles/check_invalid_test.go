package profiles

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
)

// D-128 regression: the repair-capable read lists a profile the launch admission
// refuses, so the check must report that failure instead of passing it. Otherwise
// an enabled profile with an invalid field looks healthy while Load rejects it.
func TestCheckReportsAProfileThatCannotRun(t *testing.T) {
	storetest.Isolate(t)
	reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{
		{Name: "broken", Harness: profile.HarnessMock, Taste: 0, Intelligence: 5, Cost: 5},
		{Name: "fine", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5},
	}}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	cmd := NewCommand()
	cmd.SetArgs([]string{"check"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 profile check(s) failed") {
		t.Fatalf("error = %v, want the invalid profile to fail its check: %s", err, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "cannot run: invalid") || !strings.Contains(text, "fine\n  harness") {
		t.Fatalf("check did not report the invalid profile and still check the valid one: %s", text)
	}
}
