package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/enginetest"
	harnessconfig "dyna-agent/internal/harness/config"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestConfigSyncUsesRuntimeGeneration(t *testing.T) {
	for _, kind := range []string{"mcp"} {
		for _, harness := range []string{"pi"} {
			t.Run(kind+"/"+harness, func(t *testing.T) {
				storetest.Isolate(t)
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("DYNA_BIN", "/fake/dyna") // Pi generations carry Dyna's shell server
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
				t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
				runtime := harnessruntime.New()
				calls, err := websvc.Bind([]websvc.Binding{{Name: "SyncCatalogAccount", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
					Method: func(ctx context.Context, requestKind, requestHarness, account string) (harnessruntime.ConfigSyncResult, error) {
						if requestKind != kind || requestHarness != harness {
							t.Errorf("sync request = %q, %q", requestKind, requestHarness)
						}
						return runtime.SyncCatalogAccount(authority.WithActor(ctx, authority.Actor{Role: authority.Owner}), requestKind, requestHarness, account)
					},
				}})
				if err != nil {
					t.Fatal(err)
				}
				enginetest.Serve(t, store.DataDir(), calls)
				accounts := harnessruntime.NewAccounts()
				account, err := accounts.Create(harness, "sync-test")
				if err != nil {
					t.Fatal(err)
				}
				nativeHome, err := accounts.HarnessHome(harness)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(nativeHome, 0700); err != nil {
					t.Fatal(err)
				}
				source := t.TempDir()
				if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("sync proof"), 0600); err != nil {
					t.Fatal(err)
				}
				// A skill in Dyna's catalog never enters a native generation.
				if err := harnessconfig.NewConfigManager().AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), harnessconfig.ManagedSkill{Name: "proof", Source: source, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				command := mcpCmd()
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&out)
				command.SetArgs([]string{"sync", "--harness", harness, "--account", account.ID})
				if err := command.Execute(); err != nil {
					t.Fatalf("sync failed: %v; %s", err, out.String())
				}
				var result struct {
					ID        string `json:"id"`
					Directory string `json:"directory"`
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(result.ID, "gen-v1-") {
					t.Fatalf("sync bypassed runtime generation: %s", out.String())
				}
				if _, err := os.Stat(filepath.Join(result.Directory, "private-home.json")); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(result.Directory, "skills")); !os.IsNotExist(err) {
					t.Fatalf("native generation holds a skills directory: %v", err)
				}
			})
		}
	}
}
