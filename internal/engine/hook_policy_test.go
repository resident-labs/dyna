package engine

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestHookEditsUseCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	// A non-repository stops allowed operations before any hook is changed.
	repo := t.TempDir()
	h := hooks.Hook{Name: "power", Event: hooks.AfterLand, Command: "true", Cwd: "main", Timeout: "1s", OnFail: "alert"}
	args := map[string][]any{
		"AddHook":    {repo, h},
		"EditHook":   {repo, "power", hooks.Patch{}},
		"RemoveHook": {repo, "power"},
	}
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Steward, authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			a := authority.Actor{Role: role, Session: "session", Run: "wf_test", Room: "room"}
			caller := websvc.Caller{Kind: websvc.Session, Session: a.Session}
			if role == authority.Owner {
				caller = websvc.Caller{Kind: websvc.Owner}
			}
			if role == authority.Worker {
				caller = websvc.Caller{Kind: websvc.Worker, Run: a.Run}
			}
			if role == "" {
				caller = websvc.Caller{Kind: websvc.Unverified}
				a = authority.Actor{}
			}
			ctx := localCallerContext(t, caller, a)
			for name, values := range args {
				_, err := calls[name](ctx, cliArgs(t, values...))
				if err == nil {
					t.Fatalf("%s reached a write for a non-repository", name)
				}
				refused := (strings.Contains(err.Error(), "owner or root only") || role == "" && strings.Contains(err.Error(), "could not verify"))
				wantRefusal := role != authority.Owner && role != authority.Root
				if refused != wantRefusal {
					t.Errorf("%s as %q: %v", name, role, err)
				}
			}
		})
	}
	// A paired owner carries the owner actor, without a local process caller.
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	for name, values := range args {
		_, err := calls[name](ctx, cliArgs(t, values...))
		if err == nil || strings.Contains(err.Error(), "owner or root only") {
			t.Errorf("%s paired owner: %v", name, err)
		}
	}
}
