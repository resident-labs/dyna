package contract

import (
	"os"
	"path/filepath"
	"testing"
)

// LEGACY(v1-transfer): this whole file and testdata/v1bundle, the golden v1 exporter sample. Remove after the transfer.
// TestGoldenV1BundleDecodes decodes a checked-in v1 exporter sample. Its data
// is invented. A contract edit that breaks the v1 format fails here.
func TestGoldenV1BundleDecodes(t *testing.T) {
	dir := filepath.Join("testdata", "v1bundle")
	globalFile, err := os.Open(filepath.Join(dir, "global.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer globalFile.Close()
	global, err := DecodeBundle[GlobalBundle](globalFile)
	if err != nil {
		t.Fatal(err)
	}
	roomFile, err := os.Open(filepath.Join(dir, "rooms", "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer roomFile.Close()
	room, err := DecodeBundle[RoomBundle](roomFile)
	if err != nil {
		t.Fatal(err)
	}

	if got := global.Rooms; len(got) != 1 || got[0] != "sample" {
		t.Errorf("rooms = %v, want [sample]", got)
	}
	if global.Settings.OwnerName != "Sample Owner" || global.Settings.CheckInInterval != 900000 {
		t.Errorf("settings = %+v", global.Settings)
	}
	if got := global.Profiles[0]; got.Name != "sample-builder" || got.Fallback != "sample-root" || !got.Default {
		t.Errorf("profile = %+v", got)
	}
	if got := global.Subscriptions[0].Run.Command; len(got) != 2 || got[1] != "sample.sh" {
		t.Errorf("subscription command = %v", got)
	}
	if got := global.Counts.Hosts; got != 1 {
		t.Errorf("counts.hosts = %d, want 1", got)
	}

	if room.Name != "sample" || room.Limits.MemBytes != 8<<30 {
		t.Errorf("room = %q limits = %+v", room.Name, room.Limits)
	}
	goal := room.Goals[0]
	if goal.Label != "g100" || goal.Setter.Kind != PrincipalOwner || goal.Items[0].State != ItemOpen {
		t.Errorf("goal = %+v", goal)
	}
	if got := room.Issues[0].Goal; got != "g100" || room.NextIssue != 8 {
		t.Errorf("issue goal = %q, nextIssue = %d", got, room.NextIssue)
	}
	if got := room.Handoff[0].Parent; got != "sample:lead" {
		t.Errorf("handoff parent = %q, want sample:lead", got)
	}
}
