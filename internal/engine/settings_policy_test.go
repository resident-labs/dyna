package engine

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestSettingWriteUsesCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Steward, authority.Worker, ""} {
		t.Run(string(role), func(t *testing.T) {
			baseline := settings.Defaults()
			if err := baseline.Save(); err != nil {
				t.Fatal(err)
			}
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
			_, err := calls["SetSetting"](localCallerContext(t, caller, a), cliArgs(t, settings.KeySubagentMaxDepth, "4"))
			allow := role == authority.Owner || role == authority.Root
			if allow && err != nil {
				t.Fatalf("allowed setting write: %v", err)
			}
			if !allow && (err == nil || !(strings.Contains(err.Error(), "settings set is owner or root only") || role == "" && strings.Contains(err.Error(), "could not verify"))) {
				t.Errorf("setting write as %q: %v", role, err)
			}
			got, readErr := settings.Load(settings.DefaultPath())
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := settings.DefaultSubagentMaxDepth
			if allow {
				want = 4
			}
			if got.SubagentMaxDepth != want {
				t.Errorf("stored depth %d, want %d", got.SubagentMaxDepth, want)
			}
		})
	}
	// A paired device has no local caller, and carries the owner actor.
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if _, err := calls["SetSetting"](ctx, cliArgs(t, settings.KeySubagentMaxDepth, "4")); err != nil {
		t.Fatalf("paired owner write: %v", err)
	}
	got, err := settings.Load(settings.DefaultPath())
	if err != nil || got.SubagentMaxDepth != 4 {
		t.Fatalf("paired owner stored settings: %+v %v", got, err)
	}

}
