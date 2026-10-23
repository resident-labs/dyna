package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/workflowtemplate"
)

func TestTemplateSeedUsesVerifiedContextOnLocalSocket(t *testing.T) {
	calls := testCalls(t, false)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(mode.EnvMode, "")
	t.Setenv(mode.EnvSession, "")
	dir := store.DataDir()
	t.Cleanup(func() { database.Close(dir) })
	if err := state.MigrateMirrors(dir); err != nil {
		t.Fatal(err)
	}
	if err := workflowtemplate.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	call := calls["TemplateSeed"]
	if call == nil {
		t.Fatal("TemplateSeed is not registered")
	}
	for _, role := range []authority.Role{"", authority.Owner, authority.Root, authority.Session, authority.Worker} {
		t.Run(string(role), func(t *testing.T) {
			registry := filepath.Join(t.TempDir(), "templates")
			// The fake engine supplies the verified actor, never a request argument.
			enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{
				"TemplateSeed": func(ctx context.Context, args []json.RawMessage) (any, error) {
					return call(authority.WithActor(ctx, authority.Actor{Role: role}), args)
				},
			})
			var result workflowtemplate.SeedResult
			err := engineclient.New("").Call(t.Context(), "TemplateSeed", []any{registry}, &result)
			if role == "" {
				if err == nil || !strings.Contains(err.Error(), "verified caller") {
					t.Fatalf("owner environment replaced unverified actor: %v", err)
				}
				if _, err := os.Stat(registry); !os.IsNotExist(err) {
					t.Fatalf("refused call wrote files: %v", err)
				}
				return
			}
			if err != nil || len(result.Written) < 3 {
				t.Fatalf("%s seed: %#v, %v", role, result, err)
			}
		})
	}
}

func TestTemplateSeedRefusesRemoteAccess(t *testing.T) {
	calls := testCalls(t, false)
	registry := filepath.Join(t.TempDir(), "templates")
	arg, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if _, err := calls["TemplateSeed"](ctx, []json.RawMessage{arg}); err == nil || !strings.Contains(err.Error(), "local engine socket") {
		t.Fatalf("remote seed: %v", err)
	}
	if _, err := os.Stat(registry); !os.IsNotExist(err) {
		t.Fatalf("remote call wrote files: %v", err)
	}
}

func TestTemplateSeedRefusesRelativeRegistryAndReadOnlyEngine(t *testing.T) {
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	calls := testCalls(t, false)
	call := calls["TemplateSeed"]
	enginetest.Serve(t, store.DataDir(), map[string]websvc.Call{
		"TemplateSeed": func(ctx context.Context, args []json.RawMessage) (any, error) {
			return call(authority.WithActor(ctx, authority.Actor{Role: authority.Owner}), args)
		},
	})
	if err := engineclient.New("").Call(t.Context(), "TemplateSeed", []any{"relative"}, nil); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative registry: %v", err)
	}
	readOnly := testCalls(t, true)["TemplateSeed"]
	if readOnly == nil {
		t.Fatal("TemplateSeed missing in read-only call table")
	}
	if _, err := readOnly(ctx, []json.RawMessage{json.RawMessage("\"/tmp/templates\"")}); err == nil {
		t.Fatal("read-only engine accepted seed")
	}
}
