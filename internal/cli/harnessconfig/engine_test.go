package harnessconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/harness/config"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"github.com/spf13/cobra"
)

func isolateCatalog(t *testing.T) {
	t.Helper()
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
}

func serveCatalogEngine(t *testing.T, actor authority.Actor) (string, *atomic.Int32) {
	t.Helper()
	r := harnessruntime.New()
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "AddCatalogMCP", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: r.AddCatalogMCP},
		{Name: "AddCatalogSkill", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: r.AddCatalogSkill},
		{Name: "RemoveCatalogEntry", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: r.RemoveCatalogEntry},
		{Name: "SetCatalogEntryEnabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: r.SetCatalogEntryEnabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Int32
	for name, call := range calls {
		calls[name] = func(ctx context.Context, args []json.RawMessage) (any, error) {
			called.Add(1)
			return call(authority.WithActor(ctx, actor), args)
		}
	}
	return enginetest.Serve(t, store.DataDir(), calls), &called
}

func catalogFixture(t *testing.T) (string, []byte) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := config.HarnessConfig{
		MCP:    []config.ManagedMCP{{Name: "existing", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{}}},
		Skills: []config.ManagedSkill{{Name: "existing", Source: source, EnabledHarnesses: []string{}}},
	}
	b, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	path := config.NewConfigManager().Path
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return source, b
}

func catalogArgs(kind, verb, source, harness string) []string {
	args := []string{verb, "existing"}
	if harness != "" {
		args = append(args, "--harness", harness)
	}
	if verb == "add" {
		args[1] = "new"
		if kind == "mcp" {
			args = append(args, "--command", "node")
		} else {
			args = append(args, "--source", source)
		}
	}
	return args
}

func runCatalog(ctx context.Context, kind string, args []string) (string, error) {
	cmd := &cobra.Command{Use: kind, SilenceErrors: true, SilenceUsage: true}
	AddWriteCommands(cmd, kind)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func TestCatalogEngineFailureHasNoLocalFallback(t *testing.T) {
	isolateCatalog(t)
	socket, called := serveCatalogEngine(t, authority.Actor{Role: authority.Owner})
	previous := engineClient
	t.Cleanup(func() { engineClient = previous })
	engineClient = func() *engineclient.Client {
		return engineclient.New(socket+".missing", engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	for _, kind := range []string{"mcp", "skill"} {
		for _, verb := range []string{"add", "rm", "enable", "disable"} {
			t.Run(kind+"/"+verb, func(t *testing.T) {
				source, before := catalogFixture(t)
				out, err := runCatalog(t.Context(), kind, catalogArgs(kind, verb, source, "codex"))
				if !errors.Is(err, engineclient.ErrNotRunning) || out != "" {
					t.Errorf("engine failure = %v, output = %q", err, out)
				}
				after, err := os.ReadFile(config.NewConfigManager().Path)
				if err != nil || !bytes.Equal(before, after) {
					t.Errorf("engine failure changed catalog: %v", err)
				}
			})
		}
	}
	if called.Load() != 0 {
		t.Errorf("unexpected calls = %d", called.Load())
	}
}

func TestCatalogTransportPreservesPayloadAndOutput(t *testing.T) {
	isolateCatalog(t)
	source, before := catalogFixture(t)
	type request struct {
		method string
		args   []json.RawMessage
	}
	requests := make(chan request, 10)
	calls := make(map[string]websvc.Call)
	for _, method := range []string{"AddCatalogMCP", "AddCatalogSkill", "RemoveCatalogEntry", "SetCatalogEntryEnabled"} {
		calls[method] = func(_ context.Context, args []json.RawMessage) (any, error) {
			requests <- request{method, args}
			return nil, nil
		}
	}
	enginetest.Serve(t, store.DataDir(), calls)
	for _, harness := range []string{"claude", ""} {
		enabled := []string{}
		if harness != "" {
			enabled = append(enabled, harness)
		}
		for _, kind := range []string{"mcp", "skill"} {
			for _, verb := range []string{"add", "rm", "enable", "disable"} {
				args := catalogArgs(kind, verb, source, harness)
				method := "SetCatalogEntryEnabled"
				payload := []any{kind, "existing", harness, verb == "enable"}
				if verb == "rm" {
					method, payload = "RemoveCatalogEntry", []any{kind, "existing", harness}
				} else if verb == "add" && kind == "mcp" {
					args = append(args, "--arg", " spaced ", "--arg", "second", "--env-key", "TOOL_ENV")
					method = "AddCatalogMCP"
					payload = []any{config.ManagedMCP{Name: "new", MCPDefinition: config.MCPDefinition{Command: "node", Args: []string{" spaced ", "second"}, EnvKeys: []string{"TOOL_ENV"}}, EnabledHarnesses: enabled}, harness}
				} else if verb == "add" {
					method, payload = "AddCatalogSkill", []any{config.ManagedSkill{Name: "new", Source: source, EnabledHarnesses: enabled}, harness}
				}
				out, err := runCatalog(t.Context(), kind, args)
				if err != nil || out != "Dyna catalog updated.\n" {
					t.Fatalf("edit output = %q, %v", out, err)
				}
				select {
				case got := <-requests:
					want, _ := json.Marshal(payload)
					actual, _ := json.Marshal(got.args)
					if got.method != method || !bytes.Equal(actual, want) {
						t.Errorf("request = %s %s, want %s %s", got.method, actual, method, want)
					}
				default:
					t.Error("CLI bypassed the engine")
				}
			}
		}
	}
	after, err := os.ReadFile(config.NewConfigManager().Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("CLI wrote locally: %v", err)
	}
}

func TestCatalogTransportPreservesPendingError(t *testing.T) {
	isolateCatalog(t)
	source, before := catalogFixture(t)
	const cause = "catalog saved; private generation is pending: fixture refresh failure"
	var called atomic.Int32
	calls := map[string]websvc.Call{}
	for _, method := range []string{"AddCatalogMCP", "AddCatalogSkill", "RemoveCatalogEntry", "SetCatalogEntryEnabled"} {
		calls[method] = func(context.Context, []json.RawMessage) (any, error) { called.Add(1); return nil, errors.New(cause) }
	}
	enginetest.Serve(t, store.DataDir(), calls)
	for _, kind := range []string{"mcp", "skill"} {
		for _, verb := range []string{"add", "rm", "enable", "disable"} {
			out, err := runCatalog(t.Context(), kind, catalogArgs(kind, verb, source, "codex"))
			if err == nil || err.Error() != cause || strings.Contains(out, "Dyna catalog updated.") {
				t.Errorf("pending generation error = %v, output = %q", err, out)
			}
		}
	}
	if called.Load() != 8 {
		t.Errorf("engine calls = %d, want 8", called.Load())
	}
	after, err := os.ReadFile(config.NewConfigManager().Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("CLI changed catalog after engine error: %v", err)
	}
}
