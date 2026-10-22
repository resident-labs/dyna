package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

// An active Claude account with blocked generation proves which harnesses refresh.
// It contains metadata only: no login, credentials, native executable or network.
func catalogScopeRuntime(t *testing.T) *Runtime {
	t.Helper()
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	clearRuntimeSelectors(t)
	if err := accountstore.Migrate(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	r := New()
	n, err := r.Accounts.Create("claude-code", "metadata fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Accounts.setActive("claude-code", n.ID); err != nil {
		t.Fatal(err)
	}
	r.Generations = filepath.Join(t.TempDir(), "blocked-generation")
	if err := os.WriteFile(r.Generations, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	return r
}

func catalogRetirementCanary(t *testing.T, r *Runtime) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(r.Config.Path), "skills", "load-dyna-orchestrator")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("refresh canary"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func catalogSkillSource(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestCatalogCLIRefreshScopeAndSavedError(t *testing.T) {
	r := catalogScopeRuntime(t)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	source := catalogSkillSource(t)
	for _, harness := range []string{"codex", ""} {
		for _, method := range []string{"mcp add", "skill add", "mcp rm", "skill rm", "mcp enable", "skill disable"} {
			t.Run(harness+"/"+method, func(t *testing.T) {
				baseline := config.HarnessConfig{
					MCP:    []config.ManagedMCP{{Name: "existing", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"codex"}}},
					Skills: []config.ManagedSkill{{Name: "existing", Source: source, EnabledHarnesses: []string{"codex", "claude-code"}}},
				}
				canary := catalogRetirementCanary(t, r)
				b, err := json.Marshal(baseline)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.Config.Path, b, 0600); err != nil {
					t.Fatal(err)
				}
				switch method {
				case "mcp add":
					err = r.AddCatalogMCP(ctx, config.ManagedMCP{Name: "new", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"codex"}}, harness)
				case "skill add":
					err = r.AddCatalogSkill(ctx, config.ManagedSkill{Name: "new", Source: source, EnabledHarnesses: []string{"codex"}}, harness)
				case "mcp rm":
					err = r.RemoveCatalogEntry(ctx, "mcp", "existing", harness)
				case "skill rm":
					err = r.RemoveCatalogEntry(ctx, "skill", "existing", harness)
				case "mcp enable":
					err = r.SetCatalogEntryEnabled(ctx, "mcp", "existing", harness, true)
				case "skill disable":
					err = r.SetCatalogEntryEnabled(ctx, "skill", "existing", harness, false)
				}
				if harness != "" && err != nil {
					t.Fatalf("scoped refresh reached Claude: %v", err)
				}
				if harness == "" && (err == nil || !strings.HasPrefix(err.Error(), "catalog saved; private generation is pending: generation is pending for claude-code")) {
					t.Fatalf("all-harness refresh lost pending cause: %v", err)
				}
				if _, err := os.Stat(canary); !os.IsNotExist(err) {
					t.Fatalf("refresh did not run: canary error = %v", err)
				}
				got, err := r.Config.Read()
				if err != nil {
					t.Fatal(err)
				}
				switch method {
				case "mcp add":
					if !slices.ContainsFunc(got.MCP, func(p config.ManagedMCP) bool { return p.Name == "new" }) {
						t.Error("MCP add was not saved")
					}
				case "skill add":
					if !slices.ContainsFunc(got.Skills, func(p config.ManagedSkill) bool { return p.Name == "new" }) {
						t.Error("skill add was not saved")
					}
				case "mcp rm":
					if harness == "" && len(got.MCP) != 0 || harness != "" && slices.Contains(got.MCP[0].EnabledHarnesses, harness) {
						t.Error("MCP remove was not saved")
					}
				case "skill rm":
					if harness == "" && len(got.Skills) != 0 || harness != "" && slices.Contains(got.Skills[0].EnabledHarnesses, harness) {
						t.Error("skill remove was not saved")
					}
				case "mcp enable":
					if harness == "" && !slices.Contains(got.MCP[0].EnabledHarnesses, "claude-code") {
						t.Error("MCP enable was not saved")
					}
				case "skill disable":
					if harness == "" && len(got.Skills[0].EnabledHarnesses) != 0 || harness != "" && slices.Contains(got.Skills[0].EnabledHarnesses, harness) {
						t.Error("skill disable was not saved")
					}
				}
			})
		}
	}
}

func TestCatalogCLIFailuresDoNotRefresh(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	r := &Runtime{Config: config.NewConfigManager()}
	canary := catalogRetirementCanary(t, r)
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session})
	for _, err := range []error{
		r.AddCatalogMCP(ctx, config.ManagedMCP{}, "codex"),
		r.AddCatalogSkill(ctx, config.ManagedSkill{}, ""),
		r.RemoveCatalogEntry(ctx, "mcp", "", ""),
		r.RemoveCatalogEntry(ctx, "skill", "", "codex"),
		r.SetCatalogEntryEnabled(ctx, "mcp", "", "", true),
		r.SetCatalogEntryEnabled(ctx, "skill", "", "codex", false),
	} {
		if err == nil || !strings.Contains(err.Error(), "is owner or root only") || strings.Contains(err.Error(), "catalog saved") {
			t.Fatalf("catalog refusal = %v", err)
		}
	}
	// A catalog validation failure also returns directly, before regeneration.
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err := r.AddCatalogMCP(owner, config.ManagedMCP{}, ""); err == nil || strings.Contains(err.Error(), "catalog saved") {
		t.Fatalf("catalog validation = %v", err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("failed edit refreshed: %v", err)
	}
	if _, err := os.Stat(r.Config.Path); !os.IsNotExist(err) {
		t.Fatalf("failed edit created catalog: %v", err)
	}
}

func TestBrowserCatalogAddsKeepAllHarnessRefresh(t *testing.T) {
	r := catalogScopeRuntime(t)
	ctx := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	source := catalogSkillSource(t)
	_, mcpErr := r.AddHarnessMCP(ctx, config.ManagedMCP{Name: "browser-mcp", MCPDefinition: config.MCPDefinition{Command: "node"}, EnabledHarnesses: []string{"codex"}})
	_, skillErr := r.AddHarnessSkill(ctx, config.ManagedSkill{Name: "browser-skill", Source: source, EnabledHarnesses: []string{"codex"}})
	for _, err := range []error{mcpErr, skillErr} {
		if err == nil || !strings.HasPrefix(err.Error(), "generation is pending for claude-code") || strings.Contains(err.Error(), "catalog saved") {
			t.Fatalf("browser refresh changed: %v", err)
		}
	}
}
