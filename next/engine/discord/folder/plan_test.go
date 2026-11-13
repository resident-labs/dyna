package folder

import (
	"strings"
	"testing"
)

func configuration() Snapshot {
	return Snapshot{
		Server: Server{Settings: Settings{Verification: "medium", Notifications: "mentions"}, Categories: []string{"Community"}, Roles: []Role{{Identity: Identity{ID: "100"}, Name: "@everyone"}, {Identity: Identity{ID: "101"}, Name: "Moderator", Permissions: []string{"manage_messages"}}}},
		Entries: []Entry{
			{Path: "Community", Name: "Community", Category: &Category{Identity: Identity{ID: "200"}, Channels: []string{"general", "help", "news"}, Sync: false}},
			{Path: "Community/general", Name: "general", Parent: "Community", Channel: &Channel{Identity: Identity{ID: "201"}, Type: "text", Overwrites: Overwrites{Allow: map[string][]string{"member:123": {"view_channel"}}}}},
			{Path: "Community/help", Name: "help", Parent: "Community", Channel: &Channel{Identity: Identity{ID: "202"}, Type: "forum", Tags: []string{"question", "bug"}}},
			{Path: "Community/news", Name: "news", Parent: "Community", Channel: &Channel{Identity: Identity{ID: "203"}, Type: "announcement"}},
		},
	}
}

func TestExportThenLoadPlan(t *testing.T) {
	snapshot := configuration()
	path := t.TempDir()
	if _, err := Export(path, snapshot, false); err != nil {
		t.Fatal(err)
	}
	desired, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(desired, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatal(plan.String())
	}
}

func TestPlanFieldsAndAdoption(t *testing.T) {
	snapshot := configuration()
	path := t.TempDir()
	if _, err := Export(path, snapshot, false); err != nil {
		t.Fatal(err)
	}
	desired, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	desired.Server.Settings.Verification = "high"
	desired.Server.Roles[1].Name = "Mods"
	desired.Server.Roles[1].Was = "" // ID adoption renames in place.
	for index := range desired.Entries {
		if desired.Entries[index].Name == "help" {
			desired.Entries[index].Channel.Slowmode = "10s"
		}
	}
	plan, err := BuildPlan(desired, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"verification", "name", "slowmode"} {
		if !strings.Contains(plan.String(), field) {
			t.Fatalf("missing %s in %s", field, plan.String())
		}
	}
	for _, change := range plan.Changes {
		if change.Operation == Create {
			t.Fatalf("rename created object: %+v", change)
		}
	}
}

func TestPlanMissingAdoptedNeverRecreates(t *testing.T) {
	snapshot := configuration()
	desired := Folder{Server: snapshot.Server, Entries: snapshot.Entries}
	snapshot.Entries = nil
	if _, err := BuildPlan(desired, snapshot); err == nil {
		t.Fatal("missing adoption accepted")
	}
}

func TestPlanUnmanaged(t *testing.T) {
	snapshot := configuration()
	desired := Folder{Server: snapshot.Server, Entries: []Entry{{Name: "Community", Path: "Community", Unmanaged: "Owned by hchat"}}}
	plan, err := BuildPlan(desired, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || strings.Contains(plan.String(), "not in the folder") || !strings.Contains(plan.String(), "Owned by hchat") {
		t.Fatal(plan.String())
	}
}
