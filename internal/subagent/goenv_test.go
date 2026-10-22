package subagent

import (
	"testing"

	"dyna-agent/internal/profile"
)

func TestSubagentTurnGoFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"inherited", map[string]string{}, "-tags=inherited -trimpath"},
		{"profile", map[string]string{"GOFLAGS": "-mod=readonly"}, "-mod=readonly -trimpath"},
		{"disabled", map[string]string{"GOFLAGS": "-trimpath=false"}, "-trimpath=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFLAGS", "-tags=inherited")
			original := tc.env["GOFLAGS"]
			e := newTestEnv(t, profile.Profile{
				Name: "mock", Harness: profile.HarnessCustom, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
				Command: []string{"/bin/sh", "-c", `printf '%s' "$GOFLAGS"`}, Env: tc.env,
			})
			e.joinRunners()
			info, _ := e.start("task")
			_, turn := e.wait(info.ID, 1)
			if turn.Status != TurnOK || turn.Result != tc.want {
				t.Fatalf("turn = %#v, want GOFLAGS %q", turn, tc.want)
			}
			if tc.env["GOFLAGS"] != original {
				t.Fatal("GOFLAGS leaked into the profile store")
			}
		})
	}
}
