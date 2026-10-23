package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexDeviceSample is what `codex login --device-auth` prints when its output
// is a pipe, with the color codes it writes anyway.
const codexDeviceSample = "\x1b[1mWelcome to Codex\x1b[0m\n\n" +
	"\x1b[32m1. Open this link in your browser and sign in to your account\x1b[0m\n" +
	"   https://auth.openai.com/codex/device\n\n" +
	"2. Enter this one-time code (expires in 15 minutes)\n" +
	"   \x1b[33mC7ST-5VI73\x1b[0m\n"

func TestCodexSignInParserReadsLinkAndCode(t *testing.T) {
	st := SignInState{Phase: signInStarting}
	for _, line := range strings.Split(codexDeviceSample, "\n") {
		if line = plainOutput(line); line != "" {
			parseCodexSignIn(&st, line)
		}
	}
	if st.URL != "https://auth.openai.com/codex/device" || st.Code != "C7ST-5VI73" {
		t.Fatalf("sign-in state = %+v, want the device link and code", st)
	}
	if st.Phase != signInWaiting {
		t.Fatalf("phase = %q, want %q once the link and code are known", st.Phase, signInWaiting)
	}
	// A stray line, or a link to another host, is not a sign-in link.
	for _, line := range []string{"https://example.com/codex/device", "C7ST", "code=C7ST-5VI73"} {
		if st.URL != "https://auth.openai.com/codex/device" || st.Code != "C7ST-5VI73" {
			t.Fatalf("%q changed the sign-in state: %+v", line, st)
		}
	}
}

func TestPrivateSignInSettlesWithoutTerminal(t *testing.T) {
	a := testPrivateRuntime(t)
	completed, released := 0, 0
	flow := &NativeLogin{
		Cwd: t.TempDir(),
		Complete: func(context.Context) (NativeAccount, error) {
			completed++
			return NativeAccount{Status: "ready"}, nil
		},
		Cancel: func() error { released++; return nil },
	}
	spec := signInSpec{Argv: func([]string) []string { return []string{"/bin/sh", "-c", "printf '" + codexDeviceSample + "'"} }, Parse: parseCodexSignIn}
	login, err := a.startPrivateSignIn("acct", "codex", spec, flow)
	if err != nil {
		t.Fatal(err)
	}
	if login.TerminalID != "" {
		t.Fatalf("runner opened a terminal %q", login.TerminalID)
	}
	if terms := a.terminals.List(""); len(terms) != 0 {
		t.Fatalf("runner started %d terminals, want none", len(terms))
	}
	running := a.findPrivateLogin("acct")
	if running == nil || running.signIn == nil {
		t.Fatal("runner flow is not registered")
	}
	select {
	case <-running.signIn.done:
	case <-time.After(time.Second):
		t.Fatal("runner did not settle")
	}
	st, err := a.PrivateRuntimeLoginState("acct")
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != signInSucceeded || st.URL == "" || st.Code != "C7ST-5VI73" {
		t.Fatalf("state = %+v, want a succeeded sign-in with its link and code", st)
	}
	if completed != 1 || released != 0 {
		t.Fatalf("complete=%d cancel=%d, want the account saved once", completed, released)
	}
	// The card refreshes the pane through the existing finish call.
	if rows := a.PrivateRuntimeLogins(); len(rows) != 1 || rows[0].SignIn == nil || rows[0].SignIn.Phase != signInSucceeded {
		t.Fatalf("recovered logins = %+v, want the succeeded flow", rows)
	}
}

func TestPrivateSignInCancelStopsItsProcess(t *testing.T) {
	a := testPrivateRuntime(t)
	released := 0
	printed := make(chan struct{})
	var once sync.Once
	flow := &NativeLogin{
		Cwd:      t.TempDir(),
		Cancel:   func() error { released++; return nil },
		Complete: func(context.Context) (NativeAccount, error) { return NativeAccount{}, errors.New("must not complete") },
	}
	spec := signInSpec{Argv: func([]string) []string {
		return []string{"/bin/sh", "-c", "printf '" + codexDeviceSample + "'; sleep 300"}
	}, Parse: func(st *SignInState, line string) {
		parseCodexSignIn(st, line)
		once.Do(func() { close(printed) })
	}}
	if _, err := a.startPrivateSignIn("acct", "codex", spec, flow); err != nil {
		t.Fatal(err)
	}
	running := a.findPrivateLogin("acct")
	select {
	case <-printed:
	case <-time.After(time.Second):
		t.Fatal("runner never read the login output")
	}
	if state, _ := a.PrivateRuntimeLoginState("acct"); state.Phase != signInWaiting {
		t.Fatalf("phase = %q, want %q before a cancel", state.Phase, signInWaiting)
	}
	if err := a.CancelPrivateRuntimeLogin("acct"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running.signIn.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled runner left its child running")
	}
	if released != 1 || len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatalf("cancel=%d flows=%d, want the gate released and the flow dropped", released, len(a.PrivateRuntimeLogins()))
	}
	if _, err := a.PrivateRuntimeLoginState("acct"); err == nil {
		t.Fatal("cancelled flow kept its state")
	}
}
