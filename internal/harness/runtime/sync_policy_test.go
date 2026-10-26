package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestBrowserRuntimeSyncRefusesBeforeConfigAccess(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	r := New()
	if err := os.MkdirAll(filepath.Dir(r.Config.Path), 0700); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("invalid catalog")
	if err := os.WriteFile(r.Config.Path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	for _, role := range []authority.Role{authority.Session, authority.Worker, ""} {
		_, err := r.SyncPrivateRuntime(authority.WithActor(t.Context(), authority.Actor{Role: role}), "pi")
		if err == nil || !strings.Contains(err.Error(), "SyncPrivateRuntime is owner or root only") {
			t.Errorf("browser sync as %q reached config: %v", role, err)
		}
		for _, kind := range []string{"mcp", "skill"} {
			_, err := r.SyncCatalogAccount(authority.WithActor(t.Context(), authority.Actor{Role: role}), kind, "pi", "account")
			if err == nil || !strings.Contains(err.Error(), "mcp sync is owner or root only") {
				t.Errorf("account sync as %q reached config: %v", role, err)
			}
		}
	}
	after, err := os.ReadFile(r.Config.Path)
	if err != nil || !bytes.Equal(after, invalid) {
		t.Fatalf("refusal changed catalog: %v", err)
	}
}

func TestSyncCatalogAccountScopeValidationAndSummary(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DYNA_BIN", "/fake/dyna")
	t.Setenv("SYNC_TEST_SECRET", "materialized-secret-canary")
	clearRuntimeSelectors(t)
	for _, migrate := range []func(string) error{accountstore.Migrate, preferences.Migrate} {
		if err := migrate(store.DataDir()); err != nil {
			t.Fatal(err)
		}
	}
	r := New()
	selected, err := r.Accounts.Create("pi", "explicit inactive account")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := r.Accounts.Create("claude-code", "unrelated active account")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Accounts.setActive("claude-code", unrelated.ID); err != nil {
		t.Fatal(err)
	}
	home, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	claudeHome, err := r.Accounts.HarnessHome("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(claudeHome), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeHome, []byte("blocked unrelated generation"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if err := r.Config.AddMCP(owner, config.ManagedMCP{Name: "redaction", MCPDefinition: config.MCPDefinition{Command: "node", EnvKeys: []string{"SYNC_TEST_SECRET"}}, EnabledHarnesses: []string{"pi"}}); err != nil {
		t.Fatal(err)
	}
	// Internal generation remains available without a user-operation rule.
	generation, err := r.RegenerateAccount("pi", selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if generation.Env["SYNC_TEST_SECRET"] != "materialized-secret-canary" {
		t.Fatal("fixture did not materialize the environment value")
	}
	for _, role := range []authority.Role{authority.Owner, authority.Root} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		result, err := r.SyncCatalogAccount(ctx, "mcp", "pi", selected.ID)
		if err != nil {
			t.Fatal(err)
		}
		if result.ID != generation.ID || result.Directory != generation.Directory {
			t.Fatalf("selected account result = %+v", result)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) < 2 || len(fields) > 3 || fields["id"] == nil || fields["directory"] == nil || strings.Contains(string(raw), "materialized-secret-canary") {
			t.Fatalf("unsafe summary = %s", raw)
		}
		for key := range fields {
			if key != "id" && key != "directory" && key != "partialReasons" {
				t.Errorf("summary exposes %s", key)
			}
		}
	}
	if err := r.Regenerate(""); err == nil || !strings.Contains(err.Error(), "claude-code") {
		t.Fatalf("fixture did not distinguish all-active generation: %v", err)
	}
	for _, tc := range []struct{ kind, harness, account, want string }{
		{"mcp", "claude", selected.ID, "private account belongs to another harness"},
		{"mcp", "unknown-harness", selected.ID, "adapter is not available"},
		{"mcp", "pi", "missing", "does not exist"},
		{"mcp", "pi", "", "sync requires --account"},
		{"mcp", "", selected.ID, "sync requires --harness"},
		{"other", "pi", selected.ID, "unsupported config kind"},
		// The skill catalog is global and never writes a native config.
		{"skill", "pi", selected.ID, "only MCP config syncs"},
	} {
		_, err := r.SyncCatalogAccount(owner, tc.kind, tc.harness, tc.account)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("sync validation = %v, want %q", err, tc.want)
		}
	}
}

func TestBrowserRuntimeSyncKeepsActiveScopeAndSnapshot(t *testing.T) {
	r := catalogScopeRuntime(t)
	r.privateRecommendations = func(context.Context) []RecommendedVersion { return []RecommendedVersion{} }
	for _, role := range []authority.Role{authority.Owner, authority.Root} {
		ctx := authority.WithActor(t.Context(), authority.Actor{Role: role})
		snapshot, err := r.SyncPrivateRuntime(ctx, "pi")
		if err != nil {
			t.Fatalf("scoped browser sync reached unrelated Claude: %v", err)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"bundles", "accounts", "profiles", "capabilities", "selectedVersions", "recommendedVersions", "installing"} {
			if fields[key] == nil {
				t.Errorf("browser snapshot lost %s", key)
			}
		}
		if len(snapshot.Accounts) != 1 || snapshot.Accounts[0].Harness != "claude-code" {
			t.Fatalf("browser snapshot changed: %+v", snapshot.Accounts)
		}
		_, err = r.SyncPrivateRuntime(ctx, "")
		if err == nil || !strings.Contains(err.Error(), "generation is pending for claude-code") {
			t.Fatalf("browser all-active sync = %v", err)
		}
	}
}
