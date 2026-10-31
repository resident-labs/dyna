package main

import (
	"database/sql"
	"testing"

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
	cap := capture{Repos: map[string]contract.RepoName{"/capture/checkout": "github.com/acme/repo"}}
	rooms := map[contract.RepoName][]contract.RoomName{"github.com/acme/repo": {"alpha", "beta"}}
	got, _, err := readHooks(db, rooms, cap, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []contract.RoomName{"alpha", "beta"} {
		if len(got[name]) != 1 || got[name][0].Name != "verify" {
			t.Fatalf("handoff for %s = %+v", name, got[name])
		}
	}
}
