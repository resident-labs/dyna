package engine

import (
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestDiscordFolderAuthorityAndHostPath(t *testing.T) {
	fixture := discordFixture(t)
	fixture.s.layout.Data = t.TempDir()
	front := startChild(t, fixture.s, fixture.ctx, fixture.room, "front", fixture.root)
	if _, _, problem := fixture.s.discordFolderRoute(fixture.ctx, boxCaller(front), "", ""); problem == nil {
		t.Fatal("front accessed folder")
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, _, problem := fixture.s.discordFolderRoute(fixture.ctx, owner, "", ""); problem == nil {
		t.Fatal("owner without room accepted")
	}
	for _, caller := range []contract.Principal{owner, boxCaller(fixture.root)} {
		route, path, problem := fixture.s.discordFolderRoute(fixture.ctx, caller, fixture.room, "")
		if problem != nil {
			t.Fatal(problem)
		}
		roomPath, err := fixture.s.layout.Room(fixture.room)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(roomPath, "discord") || route.Guild != "100" {
			t.Fatalf("wrong folder %s, route %+v", path, route)
		}
	}
	if _, _, problem := fixture.s.discordFolderRoute(fixture.ctx, owner, fixture.room, "999"); problem == nil {
		t.Fatal("unmapped guild accepted")
	}
}
