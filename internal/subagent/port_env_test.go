package subagent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktrees/worktreetest"
)

// A turn in a tracked worktree gets that worktree's port. Other directories get none,
// even when the parent process has port variables from another worktree.
func TestSessionTurnGetsWorktreePortEnv(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked bool
	}{
		{"tracked worktree", true},
		{"plain directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DYNA_PORT", "39999")
			t.Setenv("DYNA_PORTS", "39999-40008")
			t.Setenv("PORT", "39999")
			capture := filepath.Join(t.TempDir(), "port")
			p := profile.Profile{Name: "mock", Harness: profile.HarnessCustom, Default: true,
				Command: []string{"/bin/sh", "-c", `cat >/dev/null; printf '%s|%s' "$DYNA_PORT" "$PORT" > "$CAPTURE"; printf done`},
				Env:     map[string]string{"CAPTURE": capture}, Taste: 5, Intelligence: 5, Cost: 5}
			e := newTestEnv(t, p)
			dir, want := t.TempDir(), "|"
			if tc.tracked {
				dir = worktreetest.Tracked(t)
				rec, err := worktrees.Get(dir)
				if err != nil {
					t.Fatal(err)
				}
				want = strconv.Itoa(rec.Port) + "|" + strconv.Itoa(rec.Port)
			}
			e.joinRunners()
			info, turn := e.start("check port", func(o *StartOptions) { o.Dir = dir })
			_, done := e.wait(info.ID, turn.N)
			if done.Error != "" {
				t.Fatal(done.Error)
			}
			got, err := os.ReadFile(capture)
			if err != nil || string(got) != want {
				t.Fatalf("DYNA_PORT|PORT = %q, %v; want %q", got, err, want)
			}
		})
	}
}
