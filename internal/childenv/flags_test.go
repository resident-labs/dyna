package childenv

import "testing"

func TestTrimpath(t *testing.T) {
	for _, tc := range []struct{ flags, want string }{
		{"", "-trimpath"},
		{"-tags=unit -mod=readonly", "-tags=unit -mod=readonly -trimpath"},
		{" \t", " \t -trimpath"},
		{"-trimpath", "-trimpath"},
		{"--trimpath=false", "--trimpath=false"},
		{"-trimpath=true -tags=unit", "-trimpath=true -tags=unit"},
		{"-tags=unit\t-trimpath=false\n", "-tags=unit\t-trimpath=false\n"},
		{"'-trimpath=false'", "'-trimpath=false'"},
		{`"-trimpath"`, `"-trimpath"`},
		{"-tags=trimpath", "-tags=trimpath -trimpath"},
		{"-trimpath-extra=false", "-trimpath-extra=false -trimpath"},
		{`'-ldflags=-X main.value= -trimpath=false'`, `'-ldflags=-X main.value= -trimpath=false' -trimpath`},
		{"'unterminated", "'unterminated"},
	} {
		t.Run(tc.flags, func(t *testing.T) {
			if got := Trimpath(tc.flags); got != tc.want {
				t.Fatalf("Trimpath(%q) = %q, want %q", tc.flags, got, tc.want)
			}
			if got := Trimpath(tc.want); got != tc.want {
				t.Fatalf("repeated Trimpath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetTrimpath(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=inherited")
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"inherited", map[string]string{}, "-tags=inherited -trimpath"},
		{"profile", map[string]string{"GOFLAGS": "-mod=readonly"}, "-mod=readonly -trimpath"},
		{"empty profile", map[string]string{"GOFLAGS": ""}, "-trimpath"},
		{"explicit false", map[string]string{"GOFLAGS": "-trimpath=false"}, "-trimpath=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetTrimpath(tc.env)
			if got := tc.env["GOFLAGS"]; got != tc.want {
				t.Fatalf("GOFLAGS = %q, want %q", got, tc.want)
			}
		})
	}
}
