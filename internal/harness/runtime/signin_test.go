package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// piDeviceSample is one sign-in state that the Pi helper prints, in the same
// JSON line format it writes for the card.
const piDeviceSample = "{\"type\":\"auth_url\",\"url\":\"https://example.test/oauth\",\"instructions\":\"Sign in to Pi\"}\n"

// printSampleArgv prints the Pi sample and exits, so the runner sees a
// login that finishes on its own.
func printSampleArgv([]string) []string {
	return []string{"/bin/sh", "-c", "printf '" + piDeviceSample + "'"}
}

// holdSampleArgv prints the sample, then reads stdin, which the runner keeps
// open. The process stays alive with no sleep, and each printed line reaches
// the parser before the test continues.
func holdSampleArgv([]string) []string {
	return []string{"/bin/sh", "-c", "printf '" + piDeviceSample + "'; read held"}
}

// waitingSignal closes a channel the first time the parser reaches the waiting
// state, so a test never reads the state before the code was parsed.
func waitingSignal(once *sync.Once, reached chan struct{}, parse func(*SignInState, string)) func(*SignInState, string) {
	return func(st *SignInState, line string) {
		parse(st, line)
		if st.Phase == signInWaiting {
			once.Do(func() { close(reached) })
		}
	}
}

func TestPrivateSignInSettlesWithoutTerminal(t *testing.T) {
	a := testPrivateRuntime(t)
	runs := map[string]int{}
	flow := &NativeLogin{
		Cwd: t.TempDir(),
		Complete: func(context.Context) (NativeAccount, error) {
			runs["complete"]++
			return NativeAccount{Status: "ready"}, nil
		},
		Cancel: func() error { runs["cancel"]++; return nil },
	}
	spec := signInSpec{Argv: printSampleArgv, Parse: parsePiSignIn}
	_, err := a.startPrivateSignIn("acct", "pi", spec, flow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("acct") })
	running := a.findPrivateLogin("acct")
	if running == nil || running.signIn == nil {
		t.Fatal("runner flow is not registered")
	}
	<-running.signIn.done
	st, err := a.PrivateRuntimeLoginState("acct")
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != signInSucceeded || st.URL == "" {
		t.Fatalf("state = %+v, want a succeeded sign-in with its link", st)
	}
	if runs["complete"] != 1 || runs["cancel"] != 0 {
		t.Fatalf("complete=%d cancel=%d, want the account saved once", runs["complete"], runs["cancel"])
	}
	// The card refreshes the pane through the existing finish call.
	if rows := a.PrivateRuntimeLogins(); len(rows) != 1 || rows[0].SignIn == nil || rows[0].SignIn.Phase != signInSucceeded {
		t.Fatalf("recovered logins = %+v, want the succeeded flow", rows)
	}
}

// A capture that returns no error and still no usable account must fail the
// card, or Finish would reject the account the card calls signed in.
func TestPrivateSignInRejectsNonReadyAccount(t *testing.T) {
	a := testPrivateRuntime(t)
	runs := map[string]int{}
	flow := &NativeLogin{
		Cwd: t.TempDir(),
		Complete: func(context.Context) (NativeAccount, error) {
			runs["complete"]++
			return NativeAccount{Status: "not signed in"}, nil
		},
		Cancel: func() error { runs["cancel"]++; return nil },
	}
	spec := signInSpec{Argv: printSampleArgv, Parse: parsePiSignIn}
	if _, err := a.startPrivateSignIn("acct", "pi", spec, flow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("acct") })
	running := a.findPrivateLogin("acct")
	<-running.signIn.done
	st, err := a.PrivateRuntimeLoginState("acct")
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != signInFailed || st.Message != "private sign-in is not complete" {
		t.Fatalf("state = %+v, want a failed sign-in that explains the missing account", st)
	}
	if runs["complete"] != 1 || runs["cancel"] != 1 {
		t.Fatalf("complete=%d cancel=%d, want the gate released once", runs["complete"], runs["cancel"])
	}
}

func TestPrivateSignInCancelStopsItsProcess(t *testing.T) {
	a := testPrivateRuntime(t)
	runs := map[string]int{}
	reached := make(chan struct{})
	var once sync.Once
	flow := &NativeLogin{
		Cwd:      t.TempDir(),
		Cancel:   func() error { runs["cancel"]++; return nil },
		Complete: func(context.Context) (NativeAccount, error) { return NativeAccount{}, errors.New("must not complete") },
	}
	spec := signInSpec{
		Argv:         holdSampleArgv,
		Parse:        waitingSignal(&once, reached, parsePiSignIn),
		AcceptsPaste: true,
	}
	if _, err := a.startPrivateSignIn("acct", "pi", spec, flow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("acct") })
	running := a.findPrivateLogin("acct")
	<-reached
	if state, err := a.PrivateRuntimeLoginState("acct"); err != nil || state.Phase != signInWaiting {
		t.Fatalf("state = %+v (%v), want %q before a cancel", state, err, signInWaiting)
	}
	if err := a.CancelPrivateRuntimeLogin("acct"); err != nil {
		t.Fatal(err)
	}
	<-running.signIn.done
	if runs["cancel"] != 1 || len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatalf("cancel=%d flows=%d, want the gate released and the flow dropped", runs["cancel"], len(a.PrivateRuntimeLogins()))
	}
	if _, err := a.PrivateRuntimeLoginState("acct"); err == nil {
		t.Fatal("cancelled flow kept its state")
	}
}

// A paste box sends its code to the login's stdin, and refuses to send one
// when the flow takes no paste.
func TestPrivateSignInPasteReachesStdin(t *testing.T) {
	a := testPrivateRuntime(t)
	echoed := make(chan string, 1)
	flow := &NativeLogin{
		Cwd:      t.TempDir(),
		Cancel:   func() error { return nil },
		Complete: func(context.Context) (NativeAccount, error) { return NativeAccount{Status: "ready"}, nil },
	}
	// Without a paste box the flow refuses the code.
	plain := signInSpec{Argv: func([]string) []string { return []string{"/bin/sh", "-c", "printf 'ready'"} }}
	if _, err := a.startPrivateSignIn("plain", "pi", plain, flow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("plain") })
	if err := a.RespondPrivateRuntimeLogin("plain", "ABCD-EF12"); err == nil {
		t.Fatal("a flow without a paste box took a code")
	}
	// With a paste box the login reads the code and finishes.
	paste := signInSpec{
		Argv: func([]string) []string {
			return []string{"/bin/sh", "-c", "printf 'ready\\n'; read code; printf 'PASTED:%s\\n' \"$code\""}
		},
		Parse: func(_ *SignInState, line string) {
			if code, ok := strings.CutPrefix(line, "PASTED:"); ok {
				echoed <- code
			}
		},
		AcceptsPaste: true,
	}
	if _, err := a.startPrivateSignIn("paste", "pi", paste, flow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CancelPrivateRuntimeLogin("paste") })
	running := a.findPrivateLogin("paste")
	if err := a.RespondPrivateRuntimeLogin("paste", " ABCD-EF12 "); err != nil {
		t.Fatal(err)
	}
	if got := <-echoed; got != "ABCD-EF12" {
		t.Fatalf("login read %q, want the pasted code", got)
	}
	<-running.signIn.done
	if state, err := a.PrivateRuntimeLoginState("paste"); err != nil || state.Phase != signInSucceeded {
		t.Fatalf("state = %+v (%v), want %q after the code", state, err, signInSucceeded)
	}
}

// A newly supported harness without a card adapter must show a clear failure
// before preparing native credentials or starting a process.
func TestPrivateLoginMissingSpecShowsCardFailure(t *testing.T) {
	a, _, account := setupSharedLogin(t, "pi")
	r := testPrivateRuntime(t)
	r.Accounts = a
	spec := signInSpecs["pi"]
	delete(signInSpecs, "pi")
	t.Cleanup(func() { signInSpecs["pi"] = spec })
	login, err := r.startPrivateLogin(account.ID, "/missing/pi")
	if err != nil {
		t.Fatalf("missing adapter returned an error instead of a card: %v", err)
	}
	t.Cleanup(func() { _ = r.CancelPrivateRuntimeLogin(account.ID) })
	if login.SignIn == nil || login.SignIn.Phase != signInFailed || login.SignIn.Message != "Sign-in is not available for pi." {
		t.Fatalf("missing adapter card = %+v", login.SignIn)
	}
	if pending, running := a.SwitchStatus("pi"); pending || running != 0 {
		t.Fatal("missing adapter started native work")
	}
	state, err := r.PrivateRuntimeLoginState(account.ID)
	if err != nil || state.Phase != signInFailed {
		t.Fatalf("failed card cannot be recovered: %+v, %v", state, err)
	}
}
