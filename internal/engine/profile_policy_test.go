package engine

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestProfileWritesUseCallerPower(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	p := profile.Profile{Name: "subject", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}
	edit := configsvc.ProfileEdit{Original: p.Name, Profile: configsvc.ProfileView{Name: p.Name, Harness: p.Harness, Model: "changed", Taste: 5, Intelligence: 5, Cost: 5}}
	for _, op := range []struct {
		name, action string
		args         []any
	}{
		{"SaveProfile", "profiles add", []any{edit}},
		{"RemoveProfile", "profiles remove", []any{p.Name}},
		{"SetDefaultProfile", "profiles set-default", []any{p.Name}},
		{"SetProfileDisabled", "profiles disable", []any{p.Name, true}},
		{"SetProfileDisabled", "profiles enable", []any{p.Name, false}},
	} {
		t.Run(op.action, func(t *testing.T) {
			for _, role := range []authority.Role{authority.Owner, authority.Root, authority.Session, authority.Steward, authority.Worker, ""} {
				t.Run(string(role), func(t *testing.T) {
					base := p
					base.Disabled = op.action == "profiles enable"
					reg := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{base}}
					if err := reg.Save(); err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(reg.Path)
					if err != nil {
						t.Fatal(err)
					}
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
					_, err = calls[op.name](localCallerContext(t, caller, actor), cliArgs(t, op.args...))
					allow := role == authority.Owner || role == authority.Root
					if allow && err != nil {
						t.Fatalf("allowed profile write: %v", err)
					}
					if !allow && (err == nil || !strings.Contains(err.Error(), op.action+" is owner or root only")) {
						t.Errorf("profile write as %q: %v", role, err)
					}
					after, err := os.ReadFile(reg.Path)
					if err != nil {
						t.Fatal(err)
					}
					if !allow && !bytes.Equal(before, after) {
						t.Error("refused operation changed the profile registry")
					}
					if allow && bytes.Equal(before, after) {
						t.Error("allowed operation did not change the profile registry")
					}
				})
			}
		})
	}
}
