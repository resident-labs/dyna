package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

// A card must receive the ACP helper command just as the terminal did.
func TestAntigravityCardKeepsACPCommand(t *testing.T) {
	a, _, n := setupSharedLogin(t, "antigravity")
	spec := signInSpecs["antigravity"]
	t.Cleanup(func() { signInSpecs["antigravity"] = spec })
	var got []string
	probe := spec
	probe.Argv = func(argv []string) []string {
		got = append([]string(nil), argv...)
		return nil // Inspect command selection without starting a child.
	}
	signInSpecs["antigravity"] = probe
	r := &Runtime{Accounts: a}
	if _, err := r.startPrivateLogin(n.ID, "/fake/agy"); err == nil {
		t.Fatal("the command probe must stop before starting a child")
	}
	if len(got) < 3 || got[1] != "harness" || got[2] != "accounts" || !IsACPSignInCommand(got[1:]) ||
		argAfter(got, "--agy") != "/fake/agy" || argAfter(got, "--scope") != filepath.Join(a.Root, "antigravity", ".runtime") {
		t.Fatalf("card ran %q, want the scoped ACP helper", got)
	}
}
