package harnessconfig

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
)

func TestCatalogCommandsUseOwnerEnginePower(t *testing.T) { testCatalogEnginePower(t, authority.Owner) }
func TestCatalogCommandsUseRootEnginePower(t *testing.T)  { testCatalogEnginePower(t, authority.Root) }
func TestCatalogCommandsUseSessionEnginePower(t *testing.T) {
	testCatalogEnginePower(t, authority.Session)
}
func TestCatalogCommandsUseWorkerEnginePower(t *testing.T) {
	testCatalogEnginePower(t, authority.Worker)
}

func testCatalogEnginePower(t *testing.T, role authority.Role) {
	t.Helper()
	isolateCatalog(t)
	_, called := serveCatalogEngine(t, authority.Actor{Role: role, Session: "session", Run: "wf_test"})
	allowed := role == authority.Owner || role == authority.Root
	for _, kind := range []string{"mcp", "skill"} {
		for _, verb := range []string{"add", "rm", "enable", "disable"} {
			t.Run(kind+"/"+verb, func(t *testing.T) {
				source, before := catalogFixture(t)
				ctx := t.Context()
				if allowed {
					ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Session})
				} else if role == authority.Worker {
					ctx = authority.WithActor(ctx, authority.Actor{Role: authority.Owner})
				}
				// The session case has cleared stamps and no CLI actor.
				out, err := runCatalog(ctx, kind, catalogArgs(kind, verb, source, "codex"))
				if allowed {
					if err != nil || out != "Dyna catalog updated.\n" {
						t.Fatalf("allowed edit = %v, output = %q", err, out)
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), kind+" "+verb+" is owner or root only") {
						t.Errorf("engine role %s returned %v", role, err)
					}
					after, err := os.ReadFile(config.NewConfigManager().Path)
					if err != nil || !bytes.Equal(before, after) {
						t.Errorf("refused edit changed catalog: %v", err)
					}
				}
			})
		}
	}
	if called.Load() != 8 {
		t.Fatalf("engine calls = %d, want 8", called.Load())
	}
}
