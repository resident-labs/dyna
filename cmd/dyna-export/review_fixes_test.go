package main

import (
	"database/sql"
	"testing"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func TestCountGlobalScopesUseCapturedPaths(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE goal_rooms(room TEXT,body TEXT);
CREATE TABLE machines(enabled INTEGER,local INTEGER);
CREATE TABLE accounts(id TEXT,metadata TEXT);
CREATE TABLE account_repo_scopes(account_id TEXT,repo TEXT);
CREATE TABLE web_devices(id TEXT);
INSERT INTO accounts VALUES ('a','{"provider":"anthropic"}'),('b','{"provider":"anthropic"}');
INSERT INTO account_repo_scopes VALUES ('a','/capture/checkout'),('b','/capture/unsupported');`)
	if err != nil {
		t.Fatal(err)
	}
	cap := capture{Repos: map[string]contract.RepoName{"/capture/checkout": "github.com/acme/repo", "/capture/unsupported": "git.example/acme/repo"}}
	got, err := countGlobalSQL(db, cap)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accounts != 1 {
		t.Fatalf("accounts = %d, want 1", got.Accounts)
	}
}

func TestRepositoryHookUsesCapturedCheckout(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE hook_documents(repo TEXT,body TEXT);
INSERT INTO hook_documents VALUES ('sha256-key','{"repo":"/capture/checkout","hooks":[{"name":"verify","event":"pr-merged","command":"check"}]}');`)
	if err != nil {
		t.Fatal(err)
	}
	cap := capture{Repos: map[string]contract.RepoName{"/capture/checkout": "github.com/acme/repo", "/capture/room-worktree": "github.com/acme/repo"}}
	cap.Defaults = map[contract.RepoName]contract.RoomName{"github.com/acme/repo": "alpha"}
	if _, err := db.Exec("CREATE TABLE local_ci_modes(room_id TEXT, mode TEXT)"); err != nil {
		t.Fatal(err)
	}
	repos, err := buildRepositories(db, []room.Record{{ID: "r1", Name: "alpha", Repo: "/capture/room-worktree"}}, cap)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := readHooks(db, repos.RepoRooms, repos.PathRooms, cap, []contract.RoomName{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	cap.Repos["/capture/checkout"] = ""
	cap.HookRooms = map[string][]contract.RoomName{"/capture/checkout": {"alpha"}}
	unverified, gaps, err := readHooks(db, repos.RepoRooms, repos.PathRooms, cap, []contract.RoomName{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(unverified["alpha"]) != 1 || unverified["alpha"][0].Reason != contract.HookCheckoutUnverified || !gaps[0].UnverifiedCheckout {
		t.Fatalf("unverified hook missing handoff: %+v %+v", unverified, gaps)
	}
	delete(cap.HookRooms, "/capture/checkout")
	unverified, gaps, err = readHooks(db, repos.RepoRooms, repos.PathRooms, cap, []contract.RoomName{"alpha"})
	if err != nil || len(unverified) != 0 || !gaps[0].WithoutRoom {
		t.Fatalf("unmapped hook = %+v %+v %v", unverified, gaps, err)
	}
	if _, err := db.Exec(`INSERT INTO hook_documents VALUES ('global','{"hooks":[{"name":"global-hook","command":"check"}]}')`); err != nil {
		t.Fatal(err)
	}
	onlyPath := map[string][]contract.RoomName{"/capture/checkout": {"alpha"}}
	exact, gaps, err := readHooks(db, repos.RepoRooms, onlyPath, cap, []contract.RoomName{"alpha"})
	if err != nil || len(exact["alpha"]) != 1 || len(gaps) != 2 || !gaps[0].WithoutRoom {
		t.Fatalf("exact/global handoff = %+v %+v %v", exact, gaps, err)
	}
	for _, name := range []contract.RoomName{"alpha"} {
		if len(got[name]) != 1 || got[name][0].Name != "verify" {
			t.Fatalf("handoff for %s = %+v", name, got[name])
		}
	}
}
