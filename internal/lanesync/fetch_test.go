package lanesync

import (
	"context"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubSSHRemoteUsesHTTPS(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	url := "git@github.com:owner/repo.git"
	git(t, repo, "remote", "add", "origin", url)
	cmd := githubFetchCommand(context.Background(), repo, "origin", url, "github.com/owner/repo")
	// Neither SSH nor HTTPS credentials are available. Git must try HTTPS.
	cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=false")
	at := len(cmd.Args) - 3
	args := append([]string{}, cmd.Args[:at]...)
	cmd.Args = append(args, "-c", "protocol.https.allow=never", "fetch", "--prune", "origin")
	out, e := cmd.CombinedOutput()
	if e == nil || !strings.Contains(string(out), "transport 'https' not allowed") {
		t.Fatalf("did not select HTTPS: %v %s", e, out)
	}
	if got := strings.TrimSpace(git(t, repo, "remote", "get-url", "origin")); got != url {
		t.Fatalf("changed saved URL: %s", got)
	}
}

func TestFetchSSHAliasUsesRoomAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	repo := filepath.Join(dir, "repo")
	git(t, dir, "init", repo)
	git(t, repo, "remote", "add", "origin", "https://github.com/work/alias-test.git")
	git(t, repo, "remote", "add", "alias", "git@github.com:work/alias-test.git")
	// Block HTTPS before it reaches the network. SSH also has no usable credentials.
	git(t, repo, "config", "protocol.https.allow", "never")
	rec, e := room.DefaultStore().Create(room.CreateInput{Name: "alias-test", Repo: repo})
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(dir, "account-used")
	gh := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1 $2\" >> '" + marker + "'\n" + `case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"reader"}]}}';;
"auth token") echo room-only-token;;
"api --hostname") echo '{"permissions":{"push":true}}';;
esac
`
	if e := os.WriteFile(gh, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv(ghclient.BinaryEnv, gh)
	bin := filepath.Join(dir, "bin")
	if e := os.Mkdir(bin, 0700); e != nil {
		t.Fatal(e)
	}
	sshMarker := filepath.Join(dir, "ssh-used")
	if e := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\ntouch '"+sshMarker+"'\nexit 1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := ghclient.WithRoom(context.Background(), rec.ID)
	if e := fetch(ctx, repo, "alias"); e == nil {
		t.Fatal("HTTPS should be blocked by test configuration")
	}
	used, e := os.ReadFile(marker)
	if e != nil || !strings.Contains(string(used), "auth token") {
		t.Fatalf("alias skipped room account: %s %v", used, e)
	}
	if _, e := os.Stat(sshMarker); !os.IsNotExist(e) {
		t.Fatal("alias attempted SSH")
	}
	// An unrelated repository must keep using native credentials.
	if e := os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	git(t, repo, "remote", "add", "unrelated", "git@github.com:other/repository.git")
	if e := fetch(ctx, repo, "unrelated"); e == nil {
		t.Fatal("unrelated SSH should fail without credentials")
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("unrelated remote used room account")
	}
	if _, e := os.Stat(sshMarker); e != nil {
		t.Fatal("unrelated remote did not use native SSH")
	}
}
