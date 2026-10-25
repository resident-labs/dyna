package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
)

func TestWorkflowWorkerGoFlags(t *testing.T) {
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
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			capture := filepath.Join(t.TempDir(), "flags")
			tc.env["CAPTURE"] = capture
			original := tc.env["GOFLAGS"]
			worker := fakeworker.InstallClaude(t, `printf '%s' "$GOFLAGS" > "$CAPTURE"; printf done`)
			worker.Name, worker.Default = "custom", true
			worker.Env = mergedEnv(worker.Env, tc.env)
			store := &profile.Store{Profiles: []profile.Profile{worker}}
			if _, err := Execute(context.Background(), Options{
				ScriptSrc: `return await agent("task")`, Store: store, WorkDir: t.TempDir(),
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(capture)
			if err != nil || string(got) != tc.want {
				t.Fatalf("worker GOFLAGS = %q, %v; want %q", got, err, tc.want)
			}
			if tc.env["GOFLAGS"] != original {
				t.Fatal("GOFLAGS leaked into the profile store")
			}
		})
	}
}
