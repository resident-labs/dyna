package profile

import "testing"

func TestIsEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []Profile
		lookup   string
		want     bool
	}{
		{name: "empty", lookup: "worker"},
		{name: "missing", profiles: []Profile{{Name: "other"}}, lookup: "worker"},
		{name: "enabled", profiles: []Profile{{Name: "worker"}}, lookup: "worker", want: true},
		{name: "disabled", profiles: []Profile{{Name: "worker", Disabled: true}}, lookup: "worker"},
		{name: "case sensitive", profiles: []Profile{{Name: "Worker"}}, lookup: "worker"},
		{name: "later enabled match", profiles: []Profile{{Name: "worker", Disabled: true}, {Name: "worker"}}, lookup: "worker", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEnabled(tc.profiles, tc.lookup); got != tc.want {
				t.Fatalf("IsEnabled(%q) = %v, want %v", tc.lookup, got, tc.want)
			}
		})
	}
}
