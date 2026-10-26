package ghclient

import (
	"context"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryAccounts(t *testing.T) {
	setupRooms(t, "owner/a", "owner/b")
	t.Setenv("GH_REPO", "unscoped/other")
	dir := t.TempDir()
	exe := filepath.Join(dir, "gh")
	script := `#!/bin/sh
[ -n "$GH_REPO" ] && exit 67
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"first"},{"login":"second"}]}}';;
"auth token") case "$*" in *first*) echo first-secret;; *second*) echo second-secret;; esac;;
"api --hostname")
 case "$GH_TOKEN:$4" in
 first-secret:repos/owner/a|second-secret:repos/owner/b) echo '{"id":1}';;
 *) echo "gh: Not Found (HTTP 404)" >&2; exit 1;;
 esac;;
"pr list")
 case "$GH_TOKEN:$4" in
 first-secret:owner/a|second-secret:owner/b) echo ok;;
 *) echo 'wrong account' >&2; exit 1;;
 esac;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// A globally selected account must not decide the repository account.
	t.Setenv("GH_TOKEN", "wrong-global-secret")
	for _, repo := range []string{"owner/a", "owner/b", "owner/a", "owner/b"} {
		cmd := Command(context.Background(), exe, "", "pr", "list", "--repo", repo)
		var out strings.Builder
		cmd.Stdout = &out
		if err := Run(context.Background(), cmd); err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		if strings.TrimSpace(out.String()) != "ok" {
			t.Fatalf("%s: unexpected output", repo)
		}
	}
}

func TestRepositoryScope(t *testing.T) {
	type scopeCase struct {
		name string
		args []string
		want repository
	}
	tests := []scopeCase{
		{"repo flag", []string{"--repo=work.example/team/project", "pr", "view", "1"}, repository{"work.example", "team/project"}},
		{"short repo flag", []string{"-Rteam/project", "pr", "view", "1"}, repository{"github.com", "team/project"}},
		{"repo view target", []string{"repo", "view", "other/project"}, repository{"github.com", "other/project"}},
		{"REST", []string{"api", "--hostname", "work.example", "repos/team/project/pulls/1"}, repository{"work.example", "team/project"}},
		{"logs", []string{"run", "view", "1", "--repo", "work.example/team/project"}, repository{"work.example", "team/project"}},
		{"merge URL", []string{"pr", "merge", "--", "https://github.com/team/project/pull/4"}, repository{"github.com", "team/project"}},
		{"state value resembling repo", []string{"pr", "list", "-s", "-Rother/project"}, repository{}},
		{"milestone value resembling repo", []string{"issue", "create", "-m", "-Rother/project"}, repository{}},
		{"reviewer value resembling repo", []string{"pr", "create", "-r", "--repo=other/project"}, repository{}},
		{"global selector before merge", []string{"--repo", "team/project", "pr", "merge", "-m", "--repo", "other/project"}, repository{"github.com", "other/project"}},
		{"review comment URL", []string{"pr", "review", "--comment", "https://github.com/other/project/pull/4"}, repository{"github.com", "other/project"}},
		{"review body URL", []string{"pr", "review", "-b", "https://github.com/resident-labs/project", "--comment", "https://github.com/other/project/pull/4"}, repository{"github.com", "other/project"}},
		{"review bundled body flags", []string{"pr", "review", "-cb", "https://github.com/resident-labs/project", "https://github.com/other/project/pull/4"}, repository{"github.com", "other/project"}},
		{"review bundled repo flag", []string{"pr", "review", "-cRother/project"}, repository{"github.com", "other/project"}},
		{"review body attachment URL", []string{"pr", "review", "--body-file", "https://github.com/resident-labs/project", "-c", "https://github.com/other/project/pull/4"}, repository{"github.com", "other/project"}},
		{"issue attachment URL", []string{"issue", "create", "--attach", "https://github.com/other/project"}, repository{}},
		{"issue reason selector", []string{"issue", "close", "-r", "--repo=other/project"}, repository{}},
		{"API preview selector", []string{"api", "-p", "-Rother/project", "repos/team/project/issues"}, repository{"github.com", "team/project"}},
		{"review body file selector", []string{"pr", "review", "-F", "--repo=resident-labs/project", "-c", "--repo", "other/project"}, repository{"github.com", "other/project"}},
		{"close comment value", []string{"pr", "close", "--comment", "--repo=other/project"}, repository{}},
		{"GraphQL", []string{"api", "graphql"}, repository{}},
		{"title resembling repo flag", []string{"issue", "create", "--title", "-Rother/project"}, repository{}},
		{"short title resembling repo flag", []string{"pr", "create", "-t", "--repo=other/project"}, repository{}},
		{"body resembling host flag", []string{"issue", "create", "--body", "--hostname=other.example", "-R", "team/project"}, repository{"github.com", "team/project"}},
		{"short body URL", []string{"pr", "create", "-b", "https://other.example/other/project"}, repository{}},
		{"API field resembling repo flag", []string{"api", "repos/team/project/issues", "-f", "-Rother/project"}, repository{"github.com", "team/project"}},
		{"search resembling repo flag", []string{"issue", "list", "--search", "--repo=other/project"}, repository{}},
		{"flags after delimiter", []string{"issue", "view", "--", "--repo=other/project"}, repository{}},
	}
	for _, strategy := range []string{"-s", "-m", "-r"} {
		tests = append(tests,
			scopeCase{"merge strategy URL " + strategy, []string{"pr", "merge", strategy, "https://github.com/other/project/pull/4"}, repository{"github.com", "other/project"}},
			scopeCase{"merge strategy repo " + strategy, []string{"pr", "merge", strategy, "--repo", "other/project"}, repository{"github.com", "other/project"}},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := Command(context.Background(), "gh", "", tt.args...)
			if got := repositoryFor(context.Background(), cmd); got != tt.want {
				t.Fatalf("scope = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAccountCacheAndRedaction(t *testing.T) {
	setupRooms(t, "owner/repo")
	dir := t.TempDir()
	exe := filepath.Join(dir, "gh")
	log := filepath.Join(dir, "probes")
	t.Setenv("PROBE_LOG", log)
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo status >> "$PROBE_LOG"; echo '{"hosts":{"github.com":[{"login":"first"}]}}'; exit 1;;
"auth token") echo private-secret;;
"api --hostname") echo probe >> "$PROBE_LOG"; exit 0;;
"pr view") echo "authentication failed: $GH_TOKEN" >&2; exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cmd := Command(context.Background(), exe, "", "pr", "view", "1", "--repo", "owner/repo")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := Run(context.Background(), cmd); err == nil {
			t.Fatal("expected command failure")
		}
		if strings.Contains(stderr.String(), "private-secret") || !strings.Contains(stderr.String(), "[redacted]") {
			t.Fatalf("stderr was not redacted")
		}
		if got := Account(context.Background(), exe, "", "--repo", "owner/repo"); got != "first" {
			t.Fatalf("account = %q", got)
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "status\nprobe\n" {
		t.Fatalf("repository selection was not cached: %q", raw)
	}
}

func TestNoReadableAccountFailsClosed(t *testing.T) {
	setupRooms(t, "owner/hidden")
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"first"},{"login":"second"}]}}';;
"auth token") echo secret;;
"api --hostname") echo "secret (HTTP 404)" >&2; exit 1;;
*) echo 'must not run' >&2; exit 0;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := Command(context.Background(), exe, "", "pr", "list", "--repo", "owner/hidden")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := Run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") || stderr.Len() != 0 {
		t.Fatal("credential check exposed stderr")
	}
}

func TestBypassMiseWrapper(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-gh")
	log := filepath.Join(dir, "mise-calls")
	for name, body := range map[string]string{
		"gh":      "#!/bin/sh\n# mise use -g gh\n# mise x gh\nexit 91\n",
		"real-gh": "#!/bin/sh\necho direct\n",
		"mise":    "#!/bin/sh\necho called >> \"$MISE_CALLS\"\n[ \"$1 $2\" = 'which gh' ] || exit 92\necho \"$REAL_GH\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(BinaryEnv, "")
	t.Setenv("PATH", dir)
	t.Setenv("REAL_GH", real)
	t.Setenv("MISE_CALLS", log)
	for i := 0; i < 2; i++ {
		cmd := Command(context.Background(), "", "", "--version")
		var out strings.Builder
		cmd.Stdout = &out
		if err := Run(context.Background(), cmd); err != nil {
			t.Fatalf("wrapper ran: %v", err)
		}
		if out.String() != "direct\n" {
			t.Fatalf("output = %q", out.String())
		}
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "called\n" {
		t.Fatalf("binary resolution repeated: %q", raw)
	}
}

func setupRooms(t *testing.T, repos ...string) []room.Record {
	t.Helper()
	t.Setenv(BinaryEnv, "")
	storetest.Isolate(t)
	// No test asks GitHub whether a repository is public.
	setPublic(t, false)
	records := []room.Record{}
	for i, repo := range repos {
		dir := t.TempDir()
		for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/" + repo + ".git"}} {
			if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git: %v: %s", err, out)
			}
		}
		rec, err := room.DefaultStore().Create(room.CreateInput{Name: fmt.Sprintf("repo-%d", i), Repo: dir})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, rec)
	}
	return records
}

func TestWriteRoomChoosesWriterForPublicRepository(t *testing.T) {
	records := setupRooms(t, "work/public")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"reader"},{"login":"writer"}]}}';;
"auth token") case "$*" in *reader*) echo reader-secret;; *writer*) echo writer-secret;; esac;;
"api --hostname")
 case "$GH_TOKEN" in reader-secret) echo '{"permissions":{"push":false}}';; writer-secret) echo '{"permissions":{"push":true}}';; esac;;
"pr view") echo "$GH_TOKEN";;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := Command(context.Background(), exe, "", "pr", "view", "1", "--repo", "work/public")
	var out strings.Builder
	cmd.Stdout = &out
	if err := Run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if out.String() != "writer-secret\n" {
		t.Fatal("read-write room selected an account that cannot write")
	}
}

func setPublic(t *testing.T, public bool) {
	t.Helper()
	previous := publicRepo
	publicRepo = func(context.Context, repository) bool { return public }
	t.Cleanup(func() { publicRepo = previous })
}

// A REST read of a public repository outside every room runs without any
// credentials (D-272). Writes, other commands, other endpoints and a repository
// that is not public now are refused.
func TestPublicReadOutsideTheRoomRunsAnonymouslyAndNothingElseDoes(t *testing.T) {
	records := setupRooms(t, "resident-labs/project")
	ctx := WithRoom(context.Background(), records[0].ID)
	exe := filepath.Join(t.TempDir(), "gh")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", log)
	t.Setenv("GH_TOKEN", "ambient-secret")
	script := "#!/bin/sh\necho \"token=[$GH_TOKEN] config=$(ls -A \"$GH_CONFIG_DIR\" | wc -l) $*\" >> \"$GH_CALLS\"\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		return Run(ctx, Command(ctx, exe, records[0].Repo, args...))
	}
	var asked []string
	publicRepo = func(_ context.Context, r repository) bool { asked = append(asked, r.name); return true }
	t.Cleanup(func() { publicRepo = func(context.Context, repository) bool { return false } })
	for _, args := range [][]string{
		{"api", "repos/ludovic111/horizon/issues/1340"},
		{"api", "repos/ludovic111/horizon/issues/1340", "-X", "GET", "--jq", ".title"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("public read %q = %v, want it to run", args, err)
		}
	}
	// Each access asks again; the question names the repository the command reads.
	if strings.Join(asked, " ") != "ludovic111/horizon ludovic111/horizon" {
		t.Fatalf("visibility asked for %v, want one check per access of the endpoint's repository", asked)
	}
	for _, args := range [][]string{
		{"api", "repos/ludovic111/horizon/issues", "-X", "POST"},
		{"issue", "view", "1340", "--repo", "ludovic111/horizon"},
		{"issue", "create", "--repo", "ludovic111/horizon"},
	} {
		if err := run(args...); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
			t.Fatalf("%q = %v, want the acting-room refusal", args, err)
		}
	}
	publicRepo = func(context.Context, repository) bool { return false }
	if err := run("api", "repos/ludovic111/horizon/issues/1340"); err == nil || !strings.Contains(err.Error(), "does not belong to the acting room") {
		t.Fatalf("read of a repository that is not public now = %v, want the refusal", err)
	}
	calls, _ := os.ReadFile(log)
	want := "token=[] config=0 api repos/ludovic111/horizon/issues/1340\ntoken=[] config=0 api repos/ludovic111/horizon/issues/1340 -X GET --jq .title\n"
	if string(calls) != want {
		t.Fatalf("gh calls =\n%s\nwant only the two anonymous public reads:\n%s", calls, want)
	}
}
