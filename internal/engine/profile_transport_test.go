package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/configsvc"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestProfileCLIShapesRejectUnverifiedClaims(t *testing.T) {
	storetest.Isolate(t)
	calls := testCalls(t, false)
	ctx := callspec.WithClaim(
		localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner}),
		callspec.Claim{Session: "claimed-session"},
	)
	p := profile.Profile{Name: "new", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}
	for _, tc := range []struct {
		name string
		args []json.RawMessage
	}{
		{"UpsertProfile", cliArgs(t, p)},
		{"SetProfileUses", cliArgs(t, "new", []string{profile.UseAgents})},
		{"EditProfileField", cliArgs(t, "new", "cost", "3")},
		// These browser argument shapes remain valid and use the same policy.
		{"SaveProfile", cliArgs(t, configsvc.ProfileEdit{})},
		{"RemoveProfile", cliArgs(t, "new")},
		{"SetDefaultProfile", cliArgs(t, "new")},
		{"SetProfileDisabled", cliArgs(t, "new", true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := calls[tc.name](ctx, tc.args)
			if err == nil || !strings.Contains(err.Error(), "could not verify") {
				t.Fatalf("claimed session reached profile mutation: %v", err)
			}
		})
	}
}
