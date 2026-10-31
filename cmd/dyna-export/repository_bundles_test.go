package main

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func TestRepositoriesReportLocalCIAndUnsupportedForge(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE local_ci_modes(room_id TEXT,mode TEXT); INSERT INTO local_ci_modes VALUES ('r1','local')`); err != nil {
		t.Fatal(err)
	}
	records := []room.Record{{ID: "r1", Name: "alpha", Repo: "/github", ExtraRepos: []string{"/forge", "/unverified"}}}
	cap := capture{
		Repos:    map[string]contract.RepoName{"/github": "github.com/acme/repo", "/forge": "git.example/acme/repo"},
		Defaults: map[contract.RepoName]contract.RoomName{"github.com/acme/repo": "alpha", "git.example/acme/repo": "alpha"},
	}
	got, err := buildRepositories(db, records, cap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Repos) != 1 || got.Repos[0].Repo != "acme/repo" || got.Repos[0].CIMode != contract.CIGitHub || got.Repos[0].MergePolicy != contract.MergeManual {
		t.Fatalf("unexpected repos: %+v", got.Repos)
	}
	handoff := got.RoomHandoffs["alpha"]
	if len(handoff) != 3 || handoff[0].Reason != contract.RepoUnsupportedForge || handoff[1].Reason != contract.RepoUnverifiedIdentity || handoff[2].Reason != contract.RepoLocalCI {
		t.Fatalf("unexpected handoff: %+v", handoff)
	}
}
