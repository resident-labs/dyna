package watch

import (
	"context"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadBatchesSeparateGitHubAccounts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for i, repo := range []string{"owner/a", "owner/b"} {
		root := t.TempDir()
		for _, args := range [][]string{{"init", "-q", root}, {"-C", root, "remote", "add", "origin", "https://github.com/" + repo + ".git"}} {
			if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git: %v: %s", err, out)
			}
		}
		if _, err := room.DefaultStore().Create(room.CreateInput{Name: fmt.Sprintf("account-%d", i), Repo: root}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"first"},{"login":"second"}]}}';;
"auth token") case "$*" in *first*) echo first-secret;; *second*) echo second-secret;; esac;;
"api --hostname")
 if [ "$4" != graphql ]; then
  case "$GH_TOKEN:$4" in first-secret:repos/owner/a|second-secret:repos/owner/b) echo '{}';; *) echo "gh: Not Found (HTTP 404)" >&2; exit 1;; esac
  exit
 fi
 case "$GH_TOKEN:$*" in
 first-secret:*'name:"b"'*|second-secret:*'name:"a"'*) exit 1;;
 esac
 printf 'HTTP/2.0 200 OK\r\n\r\n'
 echo '{"data":{"r0":{"defaultBranchRef":{"name":"main","target":{"oid":"abcdef1"}},"p1":{"number":1,"title":"Account matched","state":"OPEN","headRefOid":"abcdef1"}}}}'
 ;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	g := GitHub{Executable: exe}
	records := []*Record{
		{Key: "github.com/owner/a#1", Host: "github.com", Repo: "owner/a", Number: 1},
		{Key: "github.com/owner/b#1", Host: "github.com", Repo: "owner/b", Number: 1},
	}
	result, _, err := g.readHeads(context.Background(), records)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		probe := result[r.Key]
		if probe.Err != nil || probe.Pull.Title != "Account matched" {
			t.Fatalf("%s: %v, title %q", r.Key, probe.Err, probe.Pull.Title)
		}
	}
	// Both independent account groups remain in the result.
	if len(result) != len(records) {
		t.Fatalf("got %d records", len(result))
	}
	now := time.Now()
	scope := RateLimitScope{Host: "github.com", Account: "first", Resource: "graphql"}
	g.requests = &requestCache{rates: map[string]RateLimit{scope.Key(): {PauseUntil: now.Add(time.Hour)}}, now: func() time.Time { return now }}
	result, _, err = g.readHeads(t.Context(), records)
	if err != nil {
		t.Fatal(err)
	}
	if first := result[records[0].Key]; first.Account != "first" || first.Err == nil {
		t.Fatalf("paused account identity was lost: %+v", first)
	}
	if second := result[records[1].Key]; second.Account != "second" || second.Err != nil || second.Pull.Title != "Account matched" {
		t.Fatalf("another account paused: %+v", second)
	}
}

func TestTrackedPublicationUsesItsRoomPolicy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	rooms := room.DefaultStore()
	scoped := make([]room.Record, 0, 2)
	for i, access := range []string{"read-only", "read-write"} {
		rec, err := rooms.Create(room.CreateInput{Name: fmt.Sprintf("clone-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		rec = roomfixture.Repository(t, rooms, rec.ID, "github.com", "work/shared", access)
		scoped = append(scoped, rec)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
[ "$1" = auth ] && exit 0
echo '{"id":123}'
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Service{GH: GitHub{Executable: exe, Rooms: rooms}, Store: &Store{Root: t.TempDir()}}
	for i, rec := range scoped {
		ref := Ref{"github.com", "work/shared", i + 1}
		if err := s.Store.update(func(st *State) error {
			st.Records[ref.Key()] = &Record{Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number, RoomID: rec.ID, State: "open", Watching: true}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		err := s.Comment(context.Background(), ref, "", "Review comment", true)
		if i == 0 && (err == nil || !strings.Contains(err.Error(), "read-only")) {
			t.Fatalf("read-only tracked room allowed publication: %v", err)
		}
		if i == 1 && err != nil {
			t.Fatalf("write room publication failed: %v", err)
		}
	}
}
