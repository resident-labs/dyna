package ghclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// runRoomCommand runs a room command with the token the turn carries and reports
// the account Dyna selected.
func runRoomCommand(t *testing.T, repoDir, repo, token string) (login, out string, err error) {
	t.Helper()
	cmd := Command(context.Background(), "", repoDir, "pr", "view", "1", "--repo", repo)
	cmd.Env = append(withoutTokens(cmd.Env), "GH_TOKEN="+token, "GITHUB_TOKEN="+token)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err = RunWithAccount(context.Background(), cmd, func(account string) error {
		login = account
		return nil
	})
	return login, stdout.String(), err
}

// A managed shell cannot open the desktop keyring, so a saved login gives no
// token there. The selected room account token must still serve Dyna's own
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
worker-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"worker"}';;
worker-secret:repos/work/service) printf 'HTTP/1.1 200 OK\r\n\r\n{"permissions":{"push":true}}';;
limited-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"worker"}';;
limited-secret:repos/work/service) printf 'HTTP/1.1 200 OK\r\n\r\n{"permissions":{"push":false}}';;
second-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"second"}';;
stranger-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"stranger"}';;
*) printf 'HTTP/1.1 404 Not Found\r\n\r\n{"message":"Not Found"}'; exit 1;;
esac;;
"pr view") echo "$GH_TOKEN";;
esac
`)
	// The turn starts while the owner's keyring still answers, so the room
	// account is proved and stored.
	session, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ())
	if err != nil || session.Env["GH_TOKEN"] != "" {
		t.Fatalf("turn host=%q err=%v, want the saved account inside Dyna", session.Env["GH_HOST"], err)
	}

	// The keyring is gone: the saved login gives no token, and only an
	// explicitly supplied account token can answer.
	t.Setenv("GH_KEYRING", "gone")

	// The stored choice names the same account, so the login matches; the token
	// must still hold this repository's permission.
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "worker-secret"); err != nil || login != "worker" || out != "worker-secret\n" {
		t.Fatalf("account=%q token=%q err=%v, want the room account", login, out, err)
	}

	// The stored proof belongs to the saved token. A second token for the same
	// login with less repository permission must not use that proof.
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "limited-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("limited same-login token account=%q token=%q err=%v", login, out, err)
	}

	// With no stored choice for this GitHub configuration, the command must
	// prove the login and the repository access before it uses the token.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "worker-secret"); err != nil || login != "worker" || out != "worker-secret\n" {
		t.Fatalf("unproved turn account=%q token=%q err=%v", login, out, err)
	}

	// A token that belongs to no saved login must not borrow the room account.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "stranger-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
		t.Fatalf("unrelated token account=%q token=%q err=%v", login, out, err)
	}

	// A saved login that cannot reach the room repository is refused too.
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "second-secret"); err == nil || !strings.Contains(err.Error(), "no logged-in GitHub account") {
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

// An accepted inherited token must never become a saved-account proof: the
// saved credential has its own repository scope, and a later caller with
// keyring access would trust that proof for it.
func TestSavedCredentialWinsOverAnInheritedToken(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	installFakeGH(t, `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"},{"login":"writer"}]}}';;
"auth token") [ "$GH_KEYRING" = gone ] && { echo 'keyring unavailable' >&2; exit 1; }
	case "$*" in *worker*) echo worker-read-secret;; *writer*) echo writer-secret;; esac;;
"api --hostname") case "$GH_TOKEN:$4" in
worker-write-secret:user|worker-read-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"worker"}';;
writer-secret:user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"writer"}';;
worker-write-secret:repos/work/service|writer-secret:repos/work/service) printf 'HTTP/1.1 200 OK\r\n\r\n{"permissions":{"push":true}}';;
worker-read-secret:repos/work/service) printf 'HTTP/1.1 200 OK\r\n\r\n{"permissions":{"push":false}}';;
*) printf 'HTTP/1.1 404 Not Found\r\n\r\n{"message":"Not Found"}'; exit 1;;
esac;;
"pr view") echo "$GH_TOKEN";;
esac
`)
	// The keyring is unavailable, so the saved login gives no token. The turn's
	// own token has push for the same login, so the command may use it.
	t.Setenv("GH_KEYRING", "gone")
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "worker-write-secret"); err != nil || login != "worker" || out != "worker-write-secret\n" {
		t.Fatalf("account=%q token=%q err=%v, want the inherited token", login, out, err)
	}

	// The keyring returns. The saved token for that login is read-only, so the
	// saved writer must act; the read-only token must not borrow the inherited
	// token's proof.
	t.Setenv("GH_KEYRING", "up")
	if login, out, err := runRoomCommand(t, records[0].Repo, "work/service", "worker-write-secret"); err != nil || login != "writer" || out != "writer-secret\n" {
		t.Fatalf("account=%q token=%q err=%v, want the saved writer", login, out, err)
	}
}

// A refused probe must reach the caller as a transient error with GitHub's
// reset time, and the next selection must spend no request in that window.
func TestInheritedTokenRateLimitKeepsTheResetTime(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("GH_CALLS", calls)
	installFakeGH(t, fmt.Sprintf(`#!/bin/sh
echo "$1 $2" >> "$GH_CALLS"
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") [ "$GH_KEYRING" = gone ] && { echo 'keyring unavailable' >&2; exit 1; }; echo worker-secret;;
"api --hostname") case "$4" in
user) printf 'HTTP/1.1 200 OK\r\n\r\n{"login":"worker"}';;
*) printf 'HTTP/1.1 403 Forbidden\r\nX-RateLimit-Remaining: 0\r\nX-RateLimit-Reset: %d\r\n\r\n{"message":"API rate limit exceeded"}'; exit 1;;
esac;;
"pr view") echo "$GH_TOKEN";;
esac
`, reset.Unix()))

	// A saved proof exists, but the keyring is gone and the repository check is
	// limited. The proof must survive and the reset time must reach the caller.
	cmd := Command(context.Background(), "", records[0].Repo, "pr", "view", "1", "--repo", "work/service")
	rec, err := room.DefaultStore().Resolve(records[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	key := accountKey(cmd, repository{"github.com", "work/service"}) + rec.ID + Access(context.Background(), rec)
	proveAccount(key, "worker", ghConfigFingerprint(cmd))
	t.Setenv("GH_KEYRING", "gone")
	_, _, err = runRoomCommand(t, records[0].Repo, "work/service", "worker-secret")
	checkLimited(t, err, reset)
	if got := accounts.choices[key].login; got != "worker" {
		t.Fatalf("the saved proof was deleted: %q", got)
	}
	if _, ok := storedChoice(key, ghConfigFingerprint(cmd)); !ok {
		t.Fatal("the stored proof was deleted")
	}

	// The same window must stop the next selection before it spends a request.
	before := apiCalls(t, calls)
	if _, _, err := runRoomCommand(t, records[0].Repo, "work/service", "worker-secret"); err == nil {
		t.Fatal("a limited selection succeeded")
	} else {
		checkLimited(t, err, reset)
	}
	if after := apiCalls(t, calls); after != before {
		t.Fatalf("a limited window spent %d more requests", after-before)
	}
}

// checkLimited asserts a transient rate-limit error that keeps GitHub's reset
// time and no secret.
func checkLimited(t *testing.T, err error, reset time.Time) {
	t.Helper()
	var probe *accountProbeError
	if !errors.As(err, &probe) || !probe.transient {
		t.Fatalf("error = %v, want a transient probe error", err)
	}
	if !strings.Contains(err.Error(), "rate limited") || !probe.until.Equal(reset) || strings.Contains(err.Error(), "worker-secret") {
		t.Fatalf("error = %v, reset = %v, want the reset time %v", err, probe.until, reset)
	}
}

// apiCalls counts the API requests the fake gh answered.
func apiCalls(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "api --hostname")
}
