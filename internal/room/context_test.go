package room

import (
	"errors"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/store/storetest"
)

func TestResolveContext(t *testing.T) {
	for _, tc := range []struct {
		name, processMode, stamp, ref, want string
		strict, outside, fail               bool
	}{
		{name: "external repo room", want: "repo"},
		{name: "external outside a repo", outside: true, fail: true},
		{name: "external explicit", ref: " beta ", want: "beta"},
		{name: "external stamped", stamp: "alpha", want: "alpha"},
		{name: "external override", stamp: "alpha", ref: "beta", want: "beta"},
		{name: "inherited", processMode: mode.Subagent, stamp: "alpha", want: "alpha"},
		{name: "matching explicit", processMode: mode.Subagent, stamp: "alpha", ref: " alpha ", want: "alpha"},
		{name: "blocked switch", processMode: mode.Subagent, stamp: "alpha", ref: "beta", fail: true},
		{name: "unstamped fallback", processMode: mode.Subagent, want: "repo"},
		{name: "strict unstamped", processMode: mode.Subagent, strict: true, fail: true},
		{name: "internal root starts in its room", processMode: mode.Root, stamp: "alpha", strict: true, want: "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Isolate(t)
			s := &Store{Root: t.TempDir()}
			alpha, err := s.Create(CreateInput{Name: "alpha"})
			if err != nil {
				t.Fatal(err)
			}
			beta, err := s.Create(CreateInput{Name: "beta"})
			if err != nil {
				t.Fatal(err)
			}
			repo := newRepo(t, "My_Project")
			if tc.outside {
				t.Chdir(t.TempDir())
			} else {
				t.Chdir(repo)
			}
			t.Setenv(mode.EnvMode, tc.processMode)
			if tc.processMode != "" {
				t.Setenv(mode.EnvSession, "ses_context-test")
			}
			if tc.stamp == "alpha" {
				t.Setenv(mode.EnvRoomID, alpha.ID)
				t.Setenv(mode.EnvRoom, "alpha")
			}
			got, err := s.ResolveContext(tc.ref, tc.strict)
			if (err != nil) != tc.fail {
				t.Fatalf("ResolveContext = %+v, %v", got, err)
			}
			if tc.outside && !errors.Is(err, ErrOutsideRepo) {
				t.Fatalf("outside error = %v", err)
			}
			if tc.fail {
				return
			}
			if tc.want == "repo" {
				again, err := s.ResolveContext(tc.ref, tc.strict)
				if got.Name != "my-project" || got.Repo != repo || err != nil || again.ID != got.ID {
					t.Fatalf("repo room = %+v, again %+v, %v", got, again, err)
				}
				return
			}
			want := map[string]string{"alpha": alpha.ID, "beta": beta.ID}[tc.want]
			if got.ID != want {
				t.Fatalf("room = %s, want %s", got.ID, want)
			}
		})
	}
}
