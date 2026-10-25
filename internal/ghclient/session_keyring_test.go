package ghclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// A managed shell cannot open the desktop keyring, so a saved login gives no
// token there. The token the turn already holds must still serve Dyna's own
// GitHub commands, and only when it is that saved account with access to the
// room repository.
func TestSessionServesCommandsWhenTheKeyringIsUnavailable(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", calls)
	installFakeGH(t, `#!/bin/sh
echo "$1 $2" >> "$GH_CALLS"
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"},{"login":"second"}]}}';;
"auth token") [ "$GH_KEYRING" = gone ] && { echo 'keyring unavailable' >&2; exit 1; }; echo worker-secret;;
"api --hostname") case "$GH_TOKEN:$4" in
worker-secret:user) echo worker;;
worker-secret:repos/work/service) echo '{"permissions":{"push":true}}';;
limited-secret:user) echo worker;;
limited-secret:repos/work/service) echo '{"permissions":{"push":false}}';;
second-secret:user) echo second;;
stranger-secret:user) echo stranger;;
*) echo 'gh: Not Found (HTTP 404)' >&2; exit 1;;
esac;;
"pr view") echo "$GH_TOKEN";;
esac
`)
	// The turn starts while the owner's keyring still answers, so the room
	// account is proved and stored.
	session, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ())
	if err != nil || session.Env["GH_TOKEN"] != "worker-secret" {
		t.Fatalf("turn token=%q err=%v, want the saved account", session.Env["GH_TOKEN"], err)
	}

	// The keyring is gone: the saved login gives no token, and only the token
	// the turn carries can answer.
	t.Setenv("GH_KEYRING", "gone")

	run := func(token string) (login string, out string, err error) {
		cmd := Command(context.Background(), "", records[0].Repo, "pr", "view", "1", "--repo", "work/service")
		cmd.Env = append(withoutTokens(cmd.Env), "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
		var stdout strings.Builder
		cmd.Stdout = &stdout
		err = RunWithAccount(context.Background(), cmd, func(account string) error {
			login = account
			return nil
		})
		return login, stdout.String(), err
	}

	// The stored choice names the same account, so the login matches; the token
	// must still hold this repository's permission.
	if login, out, err := run("worker-secret"); err != nil || login != "worker" || out != "worker-secret\n" {
		t.Fatalf("account=%q token=%q err=%v, want the room account", login, out, err)
	}

	// The stored proof belongs to the saved token. A second token for the same
	// login with less repository permission must not use that proof.
	if login, out, err := run("limited-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("limited same-login token account=%q token=%q err=%v", login, out, err)
	}

	// With no stored choice for this GitHub configuration, the command must
	// prove the login and the repository access before it uses the token.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := run("worker-secret"); err != nil || login != "worker" || out != "worker-secret\n" {
		t.Fatalf("unproved turn account=%q token=%q err=%v", login, out, err)
	}

	// A token that belongs to no saved login must not borrow the room account.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := run("stranger-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("unrelated token account=%q token=%q err=%v", login, out, err)
	}

	// A saved login that cannot reach the room repository is refused too.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := run("second-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("unreachable account=%q token=%q err=%v", login, out, err)
	}

	// No refused token ever reached a GitHub command.
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	views := strings.Count(string(raw), "pr view")
	if views != 2 {
		t.Fatalf("GitHub commands ran %d times, want the 2 accepted runs: %q", views, raw)
	}
}
