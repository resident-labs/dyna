package folder

import (
	"context"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

func TestUnmanagedChannelReferencesRoundTrip(t *testing.T) {
	fake := fakediscord.New(t)
	configuration := fakediscord.CommunityConfiguration()
	for index := range configuration.Channels {
		channel := &configuration.Channels[index]
		if channel.Name == "rules" || channel.Name == "mod-log" {
			channel.NSFW = true
		}
	}
	fake.SetConfiguration(configuration)
	snapshot, err := ReadSnapshot(context.Background(), &discord.Client{HTTP: fake.HTTP.Client(), Endpoint: fake.HTTP.URL, Token: fake.Token}, "100")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if _, err := Export(directory, snapshot, false); err != nil {
		t.Fatal(err)
	}
	desired, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(desired, snapshot)
	if err != nil || len(plan.Changes) != 0 {
		t.Fatalf("marker channel references failed: %+v %v", plan, err)
	}
}

func TestTopLevelUnmanagedReferences(t *testing.T) {
	folder := Folder{
		Server:  Server{Categories: []string{"external"}, Settings: Settings{Community: &Community{Rules: "rules", Updates: "rules"}}, AutoMod: []AutoMod{{Name: "alert", Alert: "rules"}}},
		Entries: []Entry{{Name: "external", Path: "external", Unmanaged: "External category"}, {Name: "rules", Path: "rules", Unmanaged: "External channel"}},
	}
	if err := validateReferences(folder); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRoleOrderAnchors(t *testing.T) {
	roles := []Role{
		{Identity: Identity{ID: "1"}, Name: "top"},
		{Identity: Identity{ID: "2"}, Name: "integration", Managed: true},
		{Identity: Identity{ID: "3"}, Name: "lower"},
		{Identity: Identity{ID: "4"}, Name: "bottom"},
	}
	live := Snapshot{Server: Server{Roles: roles}, BotRolePosition: 100}
	var plan Plan
	if err := planRoles(&plan, []Role{roles[0], roles[1], roles[3], roles[2]}, live); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("unchanged managed anchor suppressed lower swap: %+v", plan)
	}
	plan = Plan{}
	if err := planRoles(&plan, []Role{roles[0], roles[2], roles[1], roles[3]}, live); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || len(plan.Lines) != 1 || !strings.Contains(plan.Lines[0], "integration") || !strings.Contains(plan.Lines[0], "managed") {
		t.Fatalf("managed anchor moved: %+v", plan)
	}
}
