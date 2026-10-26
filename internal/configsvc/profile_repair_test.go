package configsvc

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
)

// D-128: one profile naming a harness Dyna no longer runs made ConfigProfiles
// fail, so the editor could not list, select or repair it, while the CLI repair
// path could. Management now reads the same registry for repair, and launch
// admission still refuses the profile.
func TestProfilesListARetiredHarnessForRepair(t *testing.T) {
	isolate(t)
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write the saved registry directly: the profile names a harness this
	// release does not run, which is what a removed harness leaves behind.
	saved := `{"version":3,"profiles":[{"name":"old-harness","harness":"retired-agent","taste":5,"intelligence":5,"cost":5}]}`
	if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := Profiles()
	if err != nil {
		t.Fatalf("management list refused a retired harness: %v", err)
	}
	if len(list.Profiles) != 1 || list.Profiles[0].Problem != profile.StatusRetiredHarness {
		t.Fatalf("list = %+v, want one profile with the retired-harness problem", list.Profiles)
	}
	// Launch admission keeps refusing the same registry.
	if _, err := profile.Load(path); err == nil {
		t.Fatal("Load admitted a profile of a retired harness")
	}
	// The editor repairs it through the same read.
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root})
	svc := &Service{}
	repaired, err := svc.SaveProfile(ctx, ProfileEdit{Original: "old-harness", Profile: ProfileView{Name: "old-harness", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}})
	if err != nil {
		t.Fatalf("repair through the editor: %v", err)
	}
	if repaired.Problem != profile.StatusOK {
		t.Fatalf("saved profile problem = %q, want none", repaired.Problem)
	}
	if _, err := profile.Load(path); err != nil {
		t.Fatalf("repaired registry is not launchable: %v", err)
	}
}
