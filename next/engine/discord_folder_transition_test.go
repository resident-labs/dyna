package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
	"github.com/resident-labs/dyna/engine/store"
)

func TestDiscordMappingTransitionPreservesPrivateSource(t *testing.T) {
	fixture := discordFixture(t)
	fixture.s.layout.Data = t.TempDir()
	fixture.fake.SetConfiguration(fakediscord.CommunityConfiguration())
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := fixture.s.DiscordExport(fixture.ctx, owner, contract.DiscordExportRequest{Room: fixture.room}); problem != nil {
		t.Fatal(problem)
	}
	roomPath, err := fixture.s.layout.Room(fixture.room)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(roomPath, "discord")
	privatePath := filepath.Join(root, "private-note")
	if err := os.WriteFile(privatePath, []byte("private source retained"), 0600); err != nil {
		t.Fatal(err)
	}
	second := store.DiscordRoute{Guild: "101", Room: fixture.room, Session: fixture.root}
	if err := fixture.s.store.SetDiscordRoute(fixture.ctx, second, false); err != nil {
		t.Fatal(err)
	}
	if _, problem := fixture.s.DiscordPlan(fixture.ctx, owner, contract.DiscordPlanRequest{Room: fixture.room, Server: "100"}); problem == nil || !strings.Contains(problem.Message, "temporary sibling") {
		t.Fatalf("want safe transition refusal, got %v", problem)
	}
	data, err := os.ReadFile(privatePath)
	if err != nil || string(data) != "private source retained" {
		t.Fatalf("source lost: %s %v", data, err)
	}
	// Perform the explicit transition named by the refusal, preserving all files.
	staging := filepath.Join(roomPath, "discord-staging")
	if err := os.Rename(root, staging); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staging, filepath.Join(root, "100")); err != nil {
		t.Fatal(err)
	}
	result, problem := fixture.s.DiscordPlan(fixture.ctx, owner, contract.DiscordPlanRequest{Room: fixture.room, Server: "100"})
	if problem != nil || !strings.Contains(result.Text, "0 changes") {
		t.Fatalf("migrated plan: %+v %v", result, problem)
	}
	if err := fixture.s.store.SetDiscordRoute(fixture.ctx, second, true); err != nil {
		t.Fatal(err)
	}
	_, path, problem := fixture.s.discordFolderRoute(fixture.ctx, owner, fixture.room, "100")
	if problem != nil || path != filepath.Join(root, "100") {
		t.Fatalf("removing mapping abandoned nested source: %s %v", path, problem)
	}
	if _, err := os.Stat(filepath.Join(path, "private-note")); err != nil {
		t.Fatal(err)
	}
}
