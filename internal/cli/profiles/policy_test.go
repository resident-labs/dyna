package profiles

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
)

func TestProfileCommandsUseOwnerEnginePower(t *testing.T) {
	testProfileCommandsUseEngineCallerPower(t, authority.Owner)
}

func TestProfileCommandsUseRootEnginePower(t *testing.T) {
	testProfileCommandsUseEngineCallerPower(t, authority.Root)
}

func TestProfileCommandsUseSessionEnginePower(t *testing.T) {
	testProfileCommandsUseEngineCallerPower(t, authority.Session)
}

func TestProfileCommandsUseWorkerEnginePower(t *testing.T) {
	testProfileCommandsUseEngineCallerPower(t, authority.Worker)
}

func testProfileCommandsUseEngineCallerPower(t *testing.T, role authority.Role) {
	t.Helper()
	storetest.Isolate(t)
	_, called := serveProfileEngine(t, authority.Actor{Role: role, Session: "session", Run: "wf_test"})
	allowed := role == authority.Owner || role == authority.Root
	for _, args := range profileMutationArgs() {
		t.Run(args[0], func(t *testing.T) {
			before := seedProfileRegistry(t)
			cmd := NewCommand()
			ctx := t.Context()
			if allowed {
				ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Session})
			} else if role == authority.Worker {
				ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
			}
			// The session case has cleared stamps and no actor in the CLI context.
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			err := cmd.ExecuteContext(ctx)
			if allowed {
				if err != nil {
					t.Fatalf("allowed engine command: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "profiles "+args[0]+" is owner or root only") {
					t.Errorf("engine caller %s returned %v", role, err)
				}
				after, err := os.ReadFile(profile.DefaultPath())
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("refused command changed the profile registry: %v", err)
				}
			}
		})
	}
	if called.Load() != 6 {
		t.Fatalf("engine calls = %d, want 6", called.Load())
	}
}
