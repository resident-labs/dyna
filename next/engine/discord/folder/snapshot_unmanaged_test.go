package folder

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

func TestUnmanagedConfigurationIsNotInterpreted(t *testing.T) {
	fake := fakediscord.New(t)
	config := fakediscord.CommunityConfiguration()
	for index := range config.Channels {
		if config.Channels[index].Name == "mirror" {
			config.Channels[index].Type = 13
		}
	}
	fake.SetConfiguration(config)
	client := &discord.Client{Endpoint: fake.HTTP.URL, HTTP: fake.HTTP.Client(), Token: fake.Token}
	root := t.TempDir()
	writeTestFile(t, root, "channels/external/UNMANAGED.md", "Another service owns this")
	snapshot, err := ReadExportSnapshot(context.Background(), client, "100", root, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Export(root, snapshot, true); err != nil {
		t.Fatal(err)
	}
	desired, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	current, err := ReadManagedSnapshot(context.Background(), client, "100", desired.Entries)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(desired, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatal(plan.String())
	}
	for _, request := range fake.RequestLog() {
		if request.Method != "GET" {
			t.Fatalf("unexpected write: %+v", request)
		}
	}
	for _, entry := range current.Entries {
		if entry.Path == filepath.Join("external", "mirror") {
			t.Fatal("unmanaged descendant interpreted")
		}
	}
}
