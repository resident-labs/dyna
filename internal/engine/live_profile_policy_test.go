package engine

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestLiveProfileCheckUsesCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "probe", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			actor := authority.Actor{Role: role, Session: "session", Run: "wf_test", Room: "room"}
			caller := websvc.Caller{Kind: websvc.Session, Session: actor.Session}
			if role == authority.Owner {
				caller = websvc.Caller{Kind: websvc.Owner}
			}
			if role == authority.Worker {
				caller = websvc.Caller{Kind: websvc.Worker, Run: actor.Run}
			}
			if role == "" {
				caller = websvc.Caller{Kind: websvc.Unverified}
				actor = authority.Actor{}
			}
			ctx := localCallerContext(t, caller, actor)
			// Static observations stay available through the same argument shape.
			if role != "" {
				if _, err := calls["ConfigCheckProfile"](ctx, cliArgs(t, "probe", false)); err != nil {
					t.Fatalf("static check: %v", err)
				}
			}
			// Allowed callers reach validation; permission tests start no harness.
			_, err := calls["ConfigCheckProfile"](ctx, cliArgs(t, "missing", true))
			allow := role == authority.Owner || role == authority.Root
			if allow && (err == nil || !strings.Contains(err.Error(), `no profile named "missing"`)) {
				t.Fatalf("allowed caller did not reach profile validation: %v", err)
			}
			if !allow && err == nil {
				t.Error("caller started a live probe without owner or root power")
			}
			if !allow && role != "" && err != nil && !strings.Contains(err.Error(), "ConfigCheckProfile is owner or root only") {
				t.Errorf("wrong refusal: %v", err)
			}
		})
	}
}
