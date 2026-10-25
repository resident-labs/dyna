package subagent

import (
	"dyna-agent/internal/profile"
	"dyna-agent/internal/settings"
	"testing"
)

func TestStartRootUsesConfiguredProfileAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, configured, room, want string
		disableGlobal, disableRoom   bool
		roomServiceOnly              bool
	}{
		{name: "configured", configured: "chosen", want: "chosen"},
		{name: "missing", configured: "missing", want: "mock"},
		{name: "disabled", configured: "chosen", want: "mock", disableGlobal: true},
		{name: "default", want: "opus"},
		{name: "room override", configured: "chosen", room: "room-choice", want: "room-choice"},
		{name: "room override with missing global", configured: "missing", room: "room-choice", want: "room-choice"},
		{name: "missing room uses global", configured: "chosen", room: "missing", want: "chosen"},
		{name: "disabled room uses global", configured: "chosen", room: "room-choice", want: "chosen", disableRoom: true},
		{name: "ineligible room uses global", configured: "chosen", room: "room-choice", want: "chosen", roomServiceOnly: true},
		{name: "both missing use default", configured: "missing", room: "missing", want: "mock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, chosen, roomChoice, opus := mockProfile("mock"), mockProfile("chosen"), mockProfile("room-choice"), mockProfile("opus")
			base.Default = true
			chosen.Disabled = tc.disableGlobal
			roomChoice.Disabled = tc.disableRoom
			if tc.roomServiceOnly {
				roomChoice.Uses = []string{profile.UseServices}
			}
			e := newTestEnv(t, base, chosen, roomChoice, opus)
			e.room.RootProfile = tc.room
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
