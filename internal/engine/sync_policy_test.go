package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestUserSyncCallsUseEngineAuthority(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	if err := os.MkdirAll(filepath.Dir(s.runtime.Config.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.runtime.Config.Path, []byte("invalid catalog"), 0600); err != nil {
		t.Fatal(err)
	}
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	type syncCall struct {
		name string
		args []json.RawMessage
	}
	cases := []syncCall{
		{"SyncCatalogAccount", cliArgs(t, "mcp", "codex", "selected")},
		{"SyncCatalogAccount", cliArgs(t, "skill", "codex", "selected")},
		{"SyncPrivateRuntime", cliArgs(t, "codex")},
	}
	check := func(ctx context.Context, allowed bool) {
		t.Helper()
		for _, tc := range cases {
			_, err := calls[tc.name](ctx, tc.args)
			if allowed {
				// Valid owner/root calls reach the deliberately invalid catalog.
				if err == nil || !strings.Contains(err.Error(), "invalid character") {
					t.Errorf("%s did not reach generation: %v", tc.name, err)
				}
			} else if err == nil || !(strings.Contains(err.Error(), "is owner or root only") || strings.Contains(err.Error(), "could not verify")) {
				t.Errorf("%s reached generation without power: %v", tc.name, err)
			}
		}
	}
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			actor := authority.Actor{Role: role, Session: "session", Run: "wf_test"}
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
			check(localCallerContext(t, caller, actor), role == authority.Owner || role == authority.Root)
		})
	}
	claimed := callspec.WithClaim(localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner}), callspec.Claim{Session: "claimed"})
	check(claimed, false)
	// Browser owner calls still work remotely; the new account call stays local.
	paired := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if _, err := calls["SyncPrivateRuntime"](paired, cliArgs(t, "codex")); err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("browser locality changed: %v", err)
	}
	if _, err := calls["SyncCatalogAccount"](paired, cliArgs(t, "mcp", "codex", "selected")); err == nil || !strings.Contains(err.Error(), "only through the local engine socket") {
		t.Errorf("account sync accepted remote owner: %v", err)
	}
}
