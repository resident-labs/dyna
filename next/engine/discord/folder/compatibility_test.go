package folder

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

func TestCompatibilityExportRoundTrip(t *testing.T) {
	fake := fakediscord.New(t)
	fake.SetConfiguration(fakediscord.CompatibilityConfiguration())
	client := &discord.Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}
	snapshot, err := ReadSnapshot(context.Background(), client, "100")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Warnings) != 1 {
		t.Fatalf("opaque permission warning: %v", snapshot.Warnings)
	}
	directory := t.TempDir()
	report, err := Export(directory, snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 7 {
		t.Fatalf("expected 6 generated markers and 1 warning: %v", report)
	}
	for _, name := range []string{"unknown-kind", "nsfw", "forum-defaults", "tag-metadata", "flags", "opaque-overwrite"} {
		path := filepath.Join(directory, "channels/Community", name)
		marker, err := os.ReadFile(filepath.Join(path, "UNMANAGED.md"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(marker), "not managed") {
			t.Fatal(string(marker))
		}
		if _, err := os.Stat(filepath.Join(path, "channel.yaml")); !os.IsNotExist(err) {
			t.Fatal("unmanaged channel YAML written")
		}
	}
	desired, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(desired, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("roundtrip drift: %+v", plan.Changes)
	}
	// Opaque bits survive even if the desired folder omits its exported copy.
	for index := range desired.Server.Roles {
		desired.Server.Roles[index].OtherPermissions = 0
	}
	plan, err = BuildPlan(desired, snapshot)
	if err != nil || len(plan.Changes) != 0 {
		t.Fatalf("opaque bits proposed for removal: %+v, %v", plan, err)
	}
	// Existing markers take precedence over newly generated reasons on force.
	markerPath := filepath.Join(directory, "channels/Community/nsfw/UNMANAGED.md")
	if err := os.WriteFile(markerPath, []byte("Human-owned reason\nKeep details."), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(directory, snapshot, true); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != "Human-owned reason\nKeep details." {
		t.Fatalf("marker overwritten: %s %v", marker, err)
	}
	if len(fake.Writes()) != 0 {
		t.Fatal("read-only flow wrote Discord")
	}
}

func TestManagedRolesAreReadOnly(t *testing.T) {
	current := Role{Identity: Identity{ID: "11"}, Name: "Bot", Managed: true}
	desired := current
	desired.Name = "Do not change"
	desired.Color = "#ffffff"
	live := Snapshot{Server: Server{Roles: []Role{current}}}
	var plan Plan
	if err := planRoles(&plan, []Role{desired}, live); err != nil || len(plan.Changes) != 0 {
		t.Fatalf("managed role changed: %+v %v", plan, err)
	}
	if err := planRoles(&Plan{}, []Role{{Name: "Missing", Managed: true}}, Snapshot{}); err == nil {
		t.Fatal("missing managed role created")
	}
}
