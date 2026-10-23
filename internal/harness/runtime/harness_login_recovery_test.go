package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrivateLoginRecoveryRedactsAndCancels(t *testing.T) {
	a := testPrivateRuntime(t)
	done := make(chan struct{})
	close(done)
	cancelled := 0
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": {
		signIn: &privateSignIn{state: SignInState{Phase: signInWaiting}, done: done},
		flow: &NativeLogin{
			Env: []string{"TOKEN=secret-fixture"}, Warning: "personal login warning",
			Cancel: func() error { cancelled++; return errors.New("restore warning") },
		},
	}}
	rows := a.PrivateRuntimeLogins()
	if len(rows) != 1 || rows[0].AccountID != "saved" || rows[0].SignIn == nil || rows[0].Warning != "personal login warning" {
		t.Fatal(rows)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "secret-fixture") {
		t.Fatal("login environment exposed")
	}
	if err := a.CancelPrivateRuntimeLogin("saved"); err == nil || err.Error() != "restore warning" {
		t.Fatal(err)
	}
	if cancelled != 1 || len(a.PrivateRuntimeLogins()) != 0 {
		t.Fatal("login recovery state survived cancellation")
	}
	if err := a.CancelPrivateRuntimeLogin("saved"); err != nil || cancelled != 1 {
		t.Fatal("cancellation was not idempotent", err)
	}
}

func TestPrivateLoginFinishUsesRuntimeLifetime(t *testing.T) {
	a := testPrivateRuntime(t)
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	a.SetLifetime(lifetime)
	request, cancel := context.WithCancel(context.Background())
	cancel()
	a.privateLogins = map[string]*privateRuntimeLogin{"saved": {flow: &NativeLogin{
		Complete: func(ctx context.Context) (NativeAccount, error) {
			if ctx != lifetime {
				t.Error("completion did not use runtime lifetime")
			}
			return NativeAccount{Status: "ready"}, ctx.Err()
		},
	}}}
	if _, err := a.FinishPrivateRuntimeLogin(request, "saved"); err != nil {
		t.Fatalf("request cancellation stopped native login completion: %v", err)
	}
}
