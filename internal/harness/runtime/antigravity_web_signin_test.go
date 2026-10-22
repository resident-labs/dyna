package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
)

// The Settings Sign in button ran the agy CLI, which opened a color-scheme and
// a Terms screen the owner could not finish. Antigravity now runs its ACP
// sign-in instead.
func TestAntigravityWebSignInRunsTheACPSignIn(t *testing.T) {
	dyna, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agy := "/runtime/antigravity/1.4.5/bin/agy"
	got, err := privateSignInArgv("antigravity", agy, "/accounts/antigravity/.runtime", "google:1", []string{agy})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{dyna, "harness", "accounts", "_acp-signin", "--agy", agy, "--scope", "/accounts/antigravity/.runtime", "--expect", "google:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("antigravity sign-in ran %q, want the ACP helper %q", got, want)
	}
}

// The helper must sign in to the paired HOME of the flow, not to the scope
// directory: capture and worker turns read the HOME.
func TestAntigravityWebSignInHelperGetsThePairedHome(t *testing.T) {
	a, _, n := setupSharedLogin(t, "antigravity")
	flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = flow.Cancel() }()
	r := &Runtime{Accounts: a}
	argv, err := r.privateSignInCommand(n.ID, "/fake/agy", flow)
	if err != nil {
		t.Fatal(err)
	}
	scope := argAfter(argv, "--scope")
	if scope != flow.Cwd || scope != filepath.Join(a.Root, "antigravity", ".runtime") {
		t.Fatalf("helper scope %q is not the login scope %q", scope, flow.Cwd)
	}
	home := envValue(flow.Env, "HOME")
	if home != harnesshome.Home(scope) || home == scope {
		t.Fatalf("login HOME %q is not the paired HOME of %q", home, scope)
	}
	// The shared sign-in accepts the flow's HOME and refuses the scope itself.
	err = AntigravityACPSignIn(context.Background(), "/fake/agy", scope, flow.Env, "", &strings.Builder{})
	if err == nil || strings.Contains(err.Error(), "does not match") {
		t.Fatalf("the paired HOME was refused: %v", err)
	}
	err = AntigravityACPSignIn(context.Background(), "/fake/agy", scope, []string{"HOME=" + scope}, "", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("the scope directory was accepted as HOME: %v", err)
	}
}

func argAfter(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// Every other harness keeps the command its native manager chose.
func TestOtherHarnessesKeepTheirNativeSignIn(t *testing.T) {
	for _, h := range []string{"codex", "claude-code", "opencode", "muse"} {
		argv := []string{"/runtime/" + h + "/bin/" + h, "login"}
		got, err := privateSignInArgv(h, argv[0], "/accounts/"+h+"/.runtime", "", argv)
		if err != nil || !reflect.DeepEqual(got, argv) {
			t.Fatalf("%s sign-in ran %q: %v", h, got, err)
		}
	}
}

// A machine that never ran the agy CLI sign-in has no CLI token, so there is
// nothing to save an ACP grant beside. The owner gets that step named, not the
// raw capture error.
func TestAntigravitySignInFailureNamesTheCLILogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := testPrivateRuntime(t)
	account, err := r.Accounts.Create("antigravity", "")
	if err != nil {
		t.Fatal(err)
	}
	err = r.privateSignInFailure(account.ID, native.ErrNotSignedIn)
	if err == nil || !strings.Contains(err.Error(), "dyna harness login antigravity") {
		t.Fatalf("missing CLI login was not named: %v", err)
	}
	if strings.Contains(err.Error(), native.ErrNotSignedIn.Error()) {
		t.Fatalf("the raw capture error leaked: %v", err)
	}
	if got := r.privateSignInFailure(account.ID, errors.New("native failure")); got == nil || got.Error() != "native failure" {
		t.Fatalf("a non-sign-in failure was rewritten: %v", got)
	}
	codex, err := r.Accounts.Create("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.privateSignInFailure(codex.ID, native.ErrNotSignedIn); !errors.Is(got, native.ErrNotSignedIn) {
		t.Fatalf("another harness's failure was rewritten: %v", got)
	}
}

// Until an ACP grant alone can be saved, capture names the account from the
// agy CLI token in the shared HOME. A grant for any other Google account would
// save one account with another's identity, so the sign-in is pinned to it.
func TestAntigravityACPSignInIsPinnedToTheCLITokenBesideIt(t *testing.T) {
	home := t.TempDir()
	// No CLI token: the selected account, or the first sign-in of a new one.
	for _, expect := range []string{"", "google:b"} {
		if got, err := antigravityACPExpect(home, expect); err != nil || got != expect {
			t.Fatalf("expect %q became %q: %v", expect, got, err)
		}
	}
	path := filepath.Join(home, native.AntigravityAuthFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"token":{"access_token":"fake-access"},"id_token":"e30.eyJzdWIiOiJhIn0.sig","auth_method":"consumer"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Adding account B while A's CLI token is in the HOME pins the grant to A.
	if got, err := antigravityACPExpect(home, ""); err != nil || got != "google:a" {
		t.Fatalf("a new account was not pinned to the CLI token: %q %v", got, err)
	}
	// Signing in B again while A is active is refused before Google is asked.
	if _, err := antigravityACPExpect(home, "google:b"); err == nil || !strings.Contains(err.Error(), "Switch to this account first") {
		t.Fatalf("account B signed in beside A's CLI token: %v", err)
	}
	if got, err := antigravityACPExpect(home, "google:a"); err != nil || got != "google:a" {
		t.Fatalf("the active account was refused: %q %v", got, err)
	}
}
