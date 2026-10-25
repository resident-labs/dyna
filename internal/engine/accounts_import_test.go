package engine

import (
	"reflect"
	"testing"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/subagent"
)

// TestAccountHarnessUseFailsClosedWithoutASessionStore stops an import when the
// engine cannot read who runs the harness, instead of allowing it.
func TestAccountHarnessUseFailsClosedWithoutASessionStore(t *testing.T) {
	s := services{rooms: &roomsvc.Service{}}
	if _, err := s.accountHarnessUse("codex"); err == nil {
		t.Fatal("a missing session store did not stop the in-use check")
	}
}

// TestHarnessUseNamesEnabledProfilesAndOpenSessions proves the import refuses a
// harness that a profile or a turn still runs, and names it. A disabled profile
// does not block an import by itself, but an open session on it still does.
func TestHarnessUseNamesEnabledProfilesAndOpenSessions(t *testing.T) {
	profiles := []profile.Profile{
		{Name: "sol", Harness: "codex"},
		{Name: "old", Harness: "codex", Disabled: true},
		{Name: "pi-fast", Harness: "pi"},
	}
	sessions := []subagent.Meta{
		{ID: "ses_a", Name: "worker-a", Profile: "sol"},
		{ID: "ses_b", Name: "worker-b", Profile: "old"},
		{ID: "ses_c", Name: "worker-c", Profile: "pi-fast"},
		{ID: "ses_d", Profile: "gone"},
	}
	use := harnessUse("codex", profiles, sessions)
	if !reflect.DeepEqual(use.Profiles, []importer.UseProfile{{Name: "sol", Harness: "codex"}}) {
		t.Fatalf("profiles = %+v", use.Profiles)
	}
	want := []importer.UseSession{{Ref: "worker-a", Harness: "codex"}, {Ref: "worker-b", Harness: "codex"}}
	if !reflect.DeepEqual(use.Sessions, want) {
		t.Fatalf("sessions = %+v", use.Sessions)
	}
	if refusal := harnessUse("muse", profiles, sessions).Refusal("muse"); refusal != nil {
		t.Fatalf("an unused harness is refused: %+v", refusal)
	}
}
