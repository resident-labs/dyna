package ghclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// A turn that could not prove its account must not borrow another one from the
// environment or from the GitHub CLI's saved configuration.
func TestSessionTakesNoOtherAccountWhenGitHubIsLimited(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	installFakeGH(t, fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") printf 'HTTP/2.0 403 Forbidden\nX-RateLimit-Remaining: 0\nX-RateLimit-Reset: %d\n\n{"message":"API rate limit exceeded"}\n';;
esac
`, reset.Unix()))
	scratch := t.TempDir()
	inherited := append(withoutTokens(os.Environ()), "GH_TOKEN=owner-secret", "GITHUB_TOKEN=owner-secret")
	session, err := SessionEnvironment(context.Background(), records[0].ID, scratch, "/real/dyna", inherited)
	if err != nil {
		t.Fatalf("a rate limit failed the turn: %v", err)
	}
	if session.Notice == "" {
		t.Fatal("a limited turn has no notice")
	}
	if session.Env["GH_TOKEN"] != "" || session.Env["GITHUB_TOKEN"] != "" {
		t.Fatal("an inherited token stood in for the room account")
	}
	if helper := gitConfigValue(session.Env, "credential.https://github.com/work/service.helper"); helper != "" {
		t.Fatalf("git received a credential helper: %q", helper)
	}
	config := session.Env["GH_CONFIG_DIR"]
	if !strings.HasPrefix(config, scratch) {
		t.Fatalf("GH_CONFIG_DIR = %q, want an empty directory inside the scratch dir", config)
	}
	if _, err := os.Stat(filepath.Join(config, "hosts.yml")); !os.IsNotExist(err) {
		t.Fatalf("gh can read a saved account from %q: %v", config, err)
	}
}

// Replacing a saved account's token changes the GitHub CLI configuration, so
// the account must be proved again instead of trusted for the full window.
func TestSessionForgetsTheAccountWhenTheGitHubConfigChanges(t *testing.T) {
	records := setupRooms(t, "work/service")
	config := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", config)
	hosts := filepath.Join(config, "hosts.yml")
	if err := os.WriteFile(hosts, []byte("github.com:\n    user: worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	probes := filepath.Join(t.TempDir(), "probes")
	t.Setenv("GH_PROBE_LOG", probes)
	installFakeGH(t, `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo probe >> "$GH_PROBE_LOG";;
esac
`)
	if _, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ()); err != nil {
		t.Fatal(err)
	}
	if got := probeCount(t, probes); got != 1 {
		t.Fatalf("first turn probed %d times, want 1", got)
	}
	// Same process, same account memory: only the saved token changed.
	if err := os.WriteFile(hosts, []byte("github.com:\n    user: worker\n    oauth_token: replaced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", os.Environ()); err != nil {
		t.Fatal(err)
	}
	if got := probeCount(t, probes); got != 2 {
		t.Fatalf("probes after the configuration changed = %d, want 2", got)
	}
}

// A denial is final: the stored proof must go, so a later outage cannot bring
// back the account GitHub just refused.
func TestSessionForgetsADeniedAccountSoAnOutageCannotRestoreIt(t *testing.T) {
	records := setupRooms(t, "work/service")
	if err := SetAccess(room.DefaultStore(), records[0].ID, ReadWrite); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	probes := filepath.Join(t.TempDir(), "probes")
	t.Setenv("GH_PROBE_LOG", probes)
	t.Setenv("GH_REPLY", "ok")
	installFakeGH(t, fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"worker"}]}}';;
"auth token") echo worker-secret;;
"api --hostname") echo probe >> "$GH_PROBE_LOG"; case "$GH_REPLY" in
deny) echo 'gh: Not Found (HTTP 404)' >&2; exit 1;;
limit) printf 'HTTP/2.0 403 Forbidden\nX-RateLimit-Remaining: 0\nX-RateLimit-Reset: %d\n\n{"message":"API rate limit exceeded"}\n';;
*) echo '{"permissions":{"push":true}}';;
esac;;
esac
`, reset.Unix()))
	inherited := withoutTokens(os.Environ())
	session, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", inherited)
	if err != nil || session.Env["GH_TOKEN"] != "worker-secret" {
		t.Fatalf("first turn host=%q err=%v, want the proved account", session.Env["GH_HOST"], err)
	}

	// The proof expires, and GitHub then denies the account this repository.
	forgetAccountMemory()
	db, ok := sessionStore()
	if !ok {
		t.Fatal("the stored choice cannot be read")
	}
	if _, err := db.Exec("UPDATE gh_session_accounts SET expires_at=1"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_REPLY", "deny")
	denied, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", inherited)
	if err != nil {
		t.Fatalf("a denied account failed the turn: %v", err)
	}
	if denied.Env["GH_TOKEN"] != "" || !strings.Contains(denied.Notice, "No signed-in GitHub account") {
		t.Fatalf("denied turn token=%q notice=%q, want the sign-in line", denied.Env["GH_TOKEN"], denied.Notice)
	}

	// An outage follows. It must not restore the account GitHub just denied.
	t.Setenv("GH_REPLY", "limit")
	outage, err := SessionEnvironment(context.Background(), records[0].ID, t.TempDir(), "/real/dyna", inherited)
	if err != nil {
		t.Fatalf("the outage failed the turn: %v", err)
	}
	if outage.Env["GH_TOKEN"] != "" {
		t.Fatal("the denied account came back during an outage")
	}
	if !strings.Contains(outage.Notice, "rate limited") {
		t.Fatalf("notice = %q, want the limit line", outage.Notice)
	}
}
