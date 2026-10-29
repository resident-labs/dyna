package ghclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestRoomPolicyDefaultsAndOverride(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "acme/cloudportal")
	for _, rec := range []room.Record{records[1], records[0]} {
		if got := Access(context.Background(), rec); got != ReadOnly || rec.GitHubAccess != ReadOnly {
			t.Fatalf("new room policy = %s, stored = %s; want read-only", got, rec.GitHubAccess)
		}
	}
	if got := Access(context.Background(), records[1]); got != ReadOnly {
		t.Fatalf("work repo policy = %s", got)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", log)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho called >> \"$GH_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cmd := Command(ctx, exe, "", "pr", "merge", "https://github.com/acme/cloudportal/pull/1")
	if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("write error = %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("read-only command reached gh")
	}
	if err := room.DefaultStore().UpdatePreferences(records[1].ID, func(r *room.Record) error { r.GitHubAccess = ReadWrite; return nil }); err != nil {
		t.Fatal(err)
	}
	stored, err := room.DefaultStore().Resolve(records[1].ID)
	if err != nil || stored.GitHubAccess != ReadWrite || Access(ctx, stored) != ReadWrite {
		t.Fatalf("non-resident repository did not use its stored write policy: %+v, %v", stored, err)
	}
	cmd = Command(ctx, exe, "", "pr", "merge", "https://github.com/acme/cloudportal/pull/1")
	if err := Run(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(log); err != nil || !strings.Contains(string(raw), "called") {
		t.Fatal("explicit write access did not run gh")
	}
}

func TestUnscopedRepositoryNeverReachesGitHub(t *testing.T) {
	setupRooms(t, "acme/cloudportal")
	exe := filepath.Join(t.TempDir(), "gh")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", log)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho called >> \"$GH_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := Command(context.Background(), exe, "", "pr", "list", "--repo", "acme/unrelated-private")
	if err := Run(context.Background(), cmd); err == nil || !strings.Contains(err.Error(), "requires a Dyna room") {
		t.Fatalf("scope error = %v", err)
	}
	if _, err := AccountFor(context.Background(), exe, "github.com", "acme/unrelated-private"); err == nil {
		t.Fatal("selected an account outside rooms")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("unscoped request reached gh")
	}
}

func TestReadCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--repo=work/guard", "pr", "list"}, {"-R", "work/guard", "pr", "view", "1"},
		{"repo", "view"}, {"pr", "list"}, {"pr", "view", "1"}, {"pr", "diff", "1"}, {"pr", "checks", "1"},
		{"issue", "view", "1"}, {"run", "download", "1"},
		{"api", "repos/o/r/pulls"}, {"api", "repos/o/r/pulls", "-X", "GET", "-f", "per_page=100"},
		{"api", "graphql", "-f", "query=query { viewer { login } }"},
	} {
		if !isRead(args) {
			t.Fatalf("read rejected: %q", args)
		}
	}
	for _, args := range [][]string{
		{"pr", "create"}, {"pr", "merge", "1"}, {"pr", "comment", "1"}, {"pr", "edit", "1"}, {"pr", "close", "1"},
		{"issue", "edit", "1"}, {"repo", "delete"}, {"api", "repos/o/r/issues", "-X", "POST"},
		{"api", "repos/o/r", "--method=DELETE"}, {"api", "repos/o/r", "-XPATCH"},
		{"api", "repos/o/r/issues", "--raw-field=title=test"}, {"api", "repos/o/r/issues", "-ftitle=test"},
		{"api", "repos/o/r", "--input", "body.json"},
		{"api", "graphql", "-f", "query=mutation { addComment(input:{}) { clientMutationId } }"},
	} {
		if isRead(args) {
			t.Fatalf("write allowed: %q", args)
		}
	}
}

func TestActingRoomKeepsClonePoliciesSeparate(t *testing.T) {
	records := setupRooms(t, "work/shared", "work/shared")
	for i, access := range []string{ReadWrite, ReadOnly} {
		if err := SetAccess(room.DefaultStore(), records[i].ID, access); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"pr", "merge", "https://github.com/work/shared/pull/1"}
	r := repository{"github.com", "work/shared"}
	ctx := WithRoom(context.Background(), records[1].ID)
	if err := checkPolicy(ctx, records[0].Repo, r, args); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("read-only room inherited another clone's policy: %v", err)
	}
	ctx = WithRoom(context.Background(), records[0].ID)
	if err := checkPolicy(ctx, records[1].Repo, r, args); err != nil {
		t.Fatalf("write room inherited another clone's policy: %v", err)
	}
	if err := checkPolicy(context.Background(), records[1].Repo, r, args); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("checkout policy lost: %v", err)
	}
	if _, err := roomFor(context.Background(), "", r); err == nil {
		t.Fatal("ambiguous repository accepted without a room or checkout")
	}
	if _, err := roomFor(WithRoom(context.Background(), records[0].ID), "", repository{"github.com", "other/repo"}); err == nil {
		t.Fatal("acting room accepted another repository")
	}
}

func TestAgentRepositoryWritePolicy(t *testing.T) {
	records := setupRooms(t, "fork/project")
	lookups := 0
	previous := parentRepo
	parentRepo = func(ctx context.Context, cmd *exec.Cmd, r repository) (string, bool) {
		lookups++
		if r != (repository{"github.com", "fork/project"}) {
			t.Errorf("parent lookup = %+v, want the room origin", r)
			return "", false
		}
		return fetchParentRepo(ctx, cmd, r)
	}
	t.Cleanup(func() { parentRepo = previous })
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status")
 if [ -n "$TEST_ORIGIN_WRITER" ]; then
  echo '{"hosts":{"github.com":[{"login":"writer"}]}}'
 else
  echo '{"hosts":{"github.com":[{"login":"reader"}]}}'
 fi;;
"auth token") case "$*" in *reader*) echo reader-secret;; *writer*) echo writer-secret;; esac;;
"api --hostname")
 # The metadata read without --include simulates a private fork: it needs the token.
 case "$GH_TOKEN" in reader-secret|writer-secret);; *) exit 1;; esac
 case "$4 $5:$GH_TOKEN" in
 "repos/fork/project --include:reader-secret") echo '{"permissions":{"push":false}}';;
 "repos/fork/project --include:writer-secret") echo '{"permissions":{"push":true}}';;
 "repos/upstream/project --include:reader-secret") echo '{"permissions":{"push":false}}';;
 "repos/fork/project :reader-secret") echo '{"parent":{"full_name":"upstream/project"}}';;
 *) exit 1;;
 esac;;
"issue create")
 case "$*" in
 *fork/project*) [ "$GH_TOKEN" = writer-secret ];;
 *upstream/project*) [ "$GH_TOKEN" = reader-secret ];;
 *) exit 1;;
 esac;;
*) exit 0;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, exe)
	for _, tt := range []struct {
		name        string
		access      string
		args        []string
		wantError   string
		wantLookups int
	}{
		{"read-only fork credential pr create on parent", ReadWrite, []string{"pr", "create", "--repo", "upstream/project"}, "", 1},
		{"pr edit on parent", ReadWrite, []string{"pr", "edit", "--repo", "upstream/project"}, "", 1},
		{"pr ready on parent", ReadWrite, []string{"pr", "ready", "--repo", "upstream/project"}, "", 1},
		{"issue create on origin", ReadWrite, []string{"issue", "create", "--repo", "fork/project"}, "", 0},
		{"issue create on parent", ReadWrite, []string{"issue", "create", "--repo", "upstream/project"}, "", 1},
		{"read-only issue create on origin", ReadOnly, []string{"issue", "create", "--repo", "fork/project"}, errReadOnly.Error(), 0},
		{"read-only issue create on parent", ReadOnly, []string{"issue", "create", "--repo", "upstream/project"}, errReadOnly.Error(), 0},
		{"issue comment on origin", ReadWrite, []string{"issue", "comment", "1", "--repo", "fork/project"}, errAgentGitHubWrite.Error(), 0},
		{"issue comment on parent", ReadWrite, []string{"issue", "comment", "1", "--repo", "upstream/project"}, errAgentGitHubWrite.Error(), 0},
		{"pr comment on origin", ReadWrite, []string{"pr", "comment", "1", "--repo", "fork/project"}, errAgentGitHubWrite.Error(), 0},
		{"pr comment on parent", ReadWrite, []string{"pr", "comment", "1", "--repo", "upstream/project"}, errAgentGitHubWrite.Error(), 0},
		{"pr review on origin", ReadWrite, []string{"pr", "review", "--approve", "--repo", "fork/project"}, errAgentGitHubWrite.Error(), 0},
		{"pr review on parent", ReadWrite, []string{"pr", "review", "--approve", "--repo", "upstream/project"}, errAgentGitHubWrite.Error(), 0},
		{"pr merge on parent", ReadWrite, []string{"pr", "merge", "--repo", "upstream/project"}, errAgentGitHubWrite.Error(), 0},
		{"read-only pr create on parent", ReadOnly, []string{"pr", "create", "--repo", "upstream/project"}, errReadOnly.Error(), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := SetAccess(room.DefaultStore(), records[0].ID, tt.access); err != nil {
				t.Fatal(err)
			}
			lookups = 0
			writer := ""
			for _, arg := range tt.args {
				if arg == "fork/project" {
					writer = "1"
				}
			}
			t.Setenv("TEST_ORIGIN_WRITER", writer)
			ctx := WithRoom(context.Background(), records[0].ID)
			err := checkWorkerPolicy(tt.args)
			if err == nil {
				err = Run(ctx, Command(ctx, exe, records[0].Repo, tt.args...))
			}
			if tt.wantError == "" && err != nil {
				t.Fatalf("policy error = %v", err)
			}
			if tt.wantError != "" && (err == nil || err.Error() != tt.wantError) {
				t.Fatalf("policy error = %v, want %q", err, tt.wantError)
			}
			if lookups != tt.wantLookups {
				t.Fatalf("parent lookups = %d, want %d", lookups, tt.wantLookups)
			}
		})
	}
}

// A fork-head PR's head refresh reads the upstream repository, so the account
// for that read must be chosen even though the room only knows its fork.
func TestForkHeadRefreshChoosesUpstreamReadAccount(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetUpstream(records[0].ID, "github.com/upstream/project"); err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"reader"}]}}';;
"auth token") echo reader-secret;;
"api --hostname")
 case "$4 $5:$GH_TOKEN" in
 "repos/upstream/project --include:reader-secret") echo '{"permissions":{"push":false}}';;
 *) exit 1;;
 esac;;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, exe)
	login, err := AccountFor(WithRoom(context.Background(), records[0].ID), exe, "github.com", "upstream/project")
	if err != nil || login != "reader" {
		t.Fatalf("upstream read account = %q, %v; want reader", login, err)
	}
}

func TestMergeStrategiesRejectCrossRoomTargets(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "other/project")
	ctx := WithRoom(context.Background(), records[0].ID)
	for _, strategy := range []string{"-s", "-m", "-r"} {
		for _, target := range [][]string{{"https://github.com/other/project/pull/4"}, {"--repo", "other/project", "4"}} {
			args := append([]string{"pr", "merge", strategy}, target...)
			// This executable must never run: policy rejects the actual target first.
			cmd := Command(ctx, "/missing/gh", records[0].Repo, args...)
			if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
				t.Fatalf("%q: cross-room target was not rejected: %v", args, err)
			}
		}
	}
}

func TestReviewCommentsRejectCrossRoomTargets(t *testing.T) {
	records := setupRooms(t, "resident-labs/project", "other/project")
	ctx := WithRoom(context.Background(), records[0].ID)
	for name, args := range map[string][]string{
		"comment":  {"pr", "review", "--comment", "https://github.com/other/project/pull/4"},
		"body URL": {"pr", "review", "-b", "https://github.com/resident-labs/project", "--comment", "https://github.com/other/project/pull/4"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := Command(ctx, "/missing/gh", records[0].Repo, args...)
			if err := Run(ctx, cmd); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
				t.Fatalf("cross-room review was not rejected: %v", err)
			}
		})
	}
}

// An ended context cannot prove a repository mismatch; report the context error.
func TestRoomForReportsEndedContext(t *testing.T) {
	records := setupRooms(t, "resident-labs/project")
	ctx, cancel := context.WithCancel(WithRoom(context.Background(), records[0].ID))
	cancel()
	_, err := roomFor(ctx, "", repository{"github.com", "resident-labs/project"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ended context = %v", err)
	}
}

func TestMatchesUpstreamAdmitsEveryRecordedParent(t *testing.T) {
	rec := room.Record{
		Upstream:      "github.com/acme/main",
		RepoUpstreams: map[string]string{"/work/sidecar": "github.com/horizon-rust/horizon-mixins"},
	}
	for _, name := range []string{"acme/main", "horizon-rust/horizon-mixins"} {
		if !matchesUpstream(rec, repository{host: "github.com", name: name}) {
			t.Fatalf("%s is a recorded upstream but was not admitted", name)
		}
	}
	if matchesUpstream(rec, repository{host: "github.com", name: "acme/other"}) {
		t.Fatal("an unrecorded repository was admitted as an upstream")
	}
}

func TestExtraRepoForkParentAllowsPRCreateOnItsParent(t *testing.T) {
	records := setupRooms(t, "fork/project")
	sidecar := t.TempDir()
	for _, args := range [][]string{{"init", "-q", sidecar}, {"-C", sidecar, "remote", "add", "origin", "https://github.com/fork/sidecar.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	if err := room.DefaultStore().AddRepo(records[0].ID, sidecar, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	previous := parentRepo
	parentRepo = func(ctx context.Context, cmd *exec.Cmd, r repository) (string, bool) {
		if r == (repository{"github.com", "fork/sidecar"}) {
			return "upstream/sidecar", true
		}
		return "", false
	}
	t.Cleanup(func() { parentRepo = previous })
	ctx := WithRoom(context.Background(), records[0].ID)
	if err := checkPolicy(ctx, sidecar, repository{"github.com", "upstream/sidecar"}, []string{"pr", "create", "--repo", "upstream/sidecar"}); err != nil {
		t.Fatalf("pr create on an extra fork's parent: %v", err)
	}
}

// A git fetch from a room's upstream runs without an acting room. It reads with
// the room's account and gets no write access to the upstream.
func TestRoomUpstreamReadsWithoutActingRoom(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetUpstream(records[0].ID, "github.com/upstream/project"); err != nil {
		t.Fatal(err)
	}
	upstream := repository{"github.com", "upstream/project"}
	rec, err := roomFor(context.Background(), "", upstream)
	if !errors.Is(err, errNotInRoom) || rec.ID != records[0].ID {
		t.Fatalf("roomFor upstream = %s, %v; want the room with errNotInRoom", rec.ID, err)
	}
	if err := checkPolicy(context.Background(), "", upstream, []string{"repo", "view"}); err != nil {
		t.Fatalf("read of the upstream: %v", err)
	}
	if err := checkPolicy(context.Background(), "", upstream, []string{"pr", "merge", "1"}); err == nil {
		t.Fatal("write to the upstream was admitted")
	}
}

// Two rooms can share an upstream. A command in one room's checkout resolves to that room.
func TestSharedRoomUpstreamResolvesToCheckoutRoom(t *testing.T) {
	records := setupRooms(t, "fork/project", "fork/other")
	for _, rec := range records {
		if err := room.DefaultStore().SetUpstream(rec.ID, "github.com/upstream/project"); err != nil {
			t.Fatal(err)
		}
	}
	upstream := repository{"github.com", "upstream/project"}
	rec, err := roomFor(context.Background(), records[1].Repo, upstream)
	if !errors.Is(err, errNotInRoom) || rec.ID != records[1].ID {
		t.Fatalf("roomFor from the second checkout = %s, %v; want that room with errNotInRoom", rec.ID, err)
	}
}

func TestWorkerRepoForkIsScoped(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		allowed bool
	}{
		{"fork a repository", []string{"repo", "fork", "upstream/project"}, true},
		{"fork without a clone", []string{"repo", "fork", "upstream/project", "--clone=false", "--remote=false"}, true},
		{"fork into an organization", []string{"repo", "fork", "upstream/project", "--org", "acme"}, false},
		{"fork and clone", []string{"repo", "fork", "upstream/project", "--clone"}, false},
		{"fork under a new name", []string{"repo", "fork", "upstream/project", "--fork-name", "other"}, false},
		{"fork two repositories", []string{"repo", "fork", "upstream/project", "other/project"}, false},
		{"fork without a repository", []string{"repo", "fork"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkWorkerPolicy(tt.args) == nil; got != tt.allowed {
				t.Fatalf("checkWorkerPolicy(%v) allowed = %v, want %v", tt.args, got, tt.allowed)
			}
		})
	}
}

// A fork of a recorded upstream is admitted only in a read-write room. The
// fork goes to the signed-in account, so the room must be writable.
func TestRecordedUpstreamForkNeedsWritableRoom(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetUpstream(records[0].ID, "github.com/upstream/project"); err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	upstream := repository{"github.com", "upstream/project"}
	fork := []string{"repo", "fork", "upstream/project", "--clone=false"}
	ctx := WithRoom(context.Background(), records[0].ID)
	if err := checkPolicy(ctx, "", upstream, fork); err != nil {
		t.Fatalf("fork of the recorded upstream: %v", err)
	}
	other := repository{"github.com", "other/project"}
	if err := checkPolicy(ctx, "", other, []string{"repo", "fork", "other/project", "--clone=false"}); err == nil {
		t.Fatal("fork of an unrecorded repository was admitted")
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := checkPolicy(ctx, "", upstream, fork); !errors.Is(err, errReadOnly) {
		t.Fatalf("fork in a read-only room = %v, want %v", err, errReadOnly)
	}
}

// A fork of a recorded upstream, run outside the room, uses the room's write
// account. The read account that reads the upstream cannot create the fork.
func TestRecordedUpstreamForkUsesWriteAccount(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetUpstream(records[0].ID, "github.com/upstream/project"); err != nil {
		t.Fatal(err)
	}
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status")
 if [ -n "$TEST_ORIGIN_WRITER" ]; then
  echo '{"hosts":{"github.com":[{"login":"writer"}]}}'
 else
  echo '{"hosts":{"github.com":[{"login":"reader"}]}}'
 fi;;
"auth token") case "$*" in *reader*) echo reader-secret;; *writer*) echo writer-secret;; esac;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"repo fork") [ "$GH_TOKEN" = writer-secret ];;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BinaryEnv, exe)
	t.Setenv("TEST_ORIGIN_WRITER", "1")
	ctx := context.Background()
	if err := Run(ctx, Command(ctx, exe, "", "repo", "fork", "upstream/project", "--clone=false")); err != nil {
		t.Fatalf("fork of the recorded upstream: %v", err)
	}
}
