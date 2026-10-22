package subagent

import (
	"dyna-agent/internal/settings"
	"testing"
)

func TestStartRootUsesConfiguredProfileAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, configured, want string
		disabled               bool
	}{
		{"configured", "chosen", "chosen", false},
		{"missing", "missing", "mock", false},
		{"disabled", "chosen", "mock", true},
		{"default", "", "opus", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, chosen, opus := mockProfile("mock"), mockProfile("chosen"), mockProfile("opus")
			base.Default = true
			chosen.Disabled = tc.disabled
			e := newTestEnv(t, base, chosen, opus)
			v := settings.Defaults()
			if tc.configured != "" {
				if err := v.Set("root.profile", tc.configured); err != nil {
					t.Fatal(err)
				}
			}
			e.m.Settings = func() settings.Values { return v }
			info, started, err := e.m.StartRoot(e.room, "RESPOND: ready", "")
			if err != nil || !started || info.Profile != tc.want {
				t.Fatalf("StartRoot = profile %q, started %v, error %v; want %q", info.Profile, started, err, tc.want)
			}
			e.wait(info.ID, 1)
		})
	}
}
