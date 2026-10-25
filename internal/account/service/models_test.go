package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/account/service/adapters"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// Model discovery runs Dyna's private copy with the active account's home.
func privateCodexAccount(t *testing.T) (*runtime.Accounts, runtime.Bundle, runtime.NativeAccount, *accountstore.Store) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "owner"))
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	bundles := &runtime.BundleManager{Root: filepath.Join(root, "harnesses"), Run: func(_ context.Context, args, _ []string) error {
		prefix := args[4]
		pkg := filepath.Join(prefix, "lib", "node_modules", "@openai", "codex")
		if err := os.MkdirAll(pkg, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pkg, "cli.js"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			return err
		}
		if err := os.Symlink("../lib/node_modules/@openai/codex/cli.js", filepath.Join(prefix, "bin", "codex")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@openai/codex","version":"1.0.0"}`), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(prefix, "package-lock.json"), []byte(`{"packages":{"node_modules/@openai/codex":{"version":"1.0.0","integrity":"sha512-Zml4dHVyZQ=="}}}`), 0600)
	}}
	bundle, err := bundles.Install(context.Background(), "codex", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	store := &accountstore.Store{Root: filepath.Join(root, "accounts")}
	accounts := &runtime.Accounts{Root: store.Root}
	account, err := accounts.Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"fixture-user","chatgpt_account_id":"fixture-workspace"}}`))
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": "header." + claims + ".signature", "access_token": "fixture-access", "refresh_token": "fixture-refresh", "account_id": "fixture-workspace"}})
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Capture(context.Background(), account.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	if err := accounts.Switch(context.Background(), account.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	return accounts, bundle, account, store
}

func TestModelsUsePrivateCopyAndActiveAccount(t *testing.T) {
	accounts, bundle, _, store := privateCodexAccount(t)
	var executable string
	var env []string
	s := New(Options{Store: store, Models: func(_ context.Context, _, exe string, e []string) ([]adapters.Model, error) {
		executable, env = exe, e
		return []adapters.Model{{ID: "gpt-fixture"}}, nil
	}})
	if _, err := s.ProfileModels(context.Background(), "codex", true); err != nil {
		t.Fatal(err)
	}
	home, _ := accounts.HarnessHome("codex")
	if executable != bundle.Executable || !slices.ContainsFunc(env, func(pair string) bool { return strings.HasPrefix(pair, "CODEX_HOME=") && strings.Contains(pair, home) }) {
		t.Fatalf("Models ran %q with env %v; want the private copy and the active account's home", executable, env)
	}
}

func TestNativeAccountProbeFailureDoesNotRequestSignIn(t *testing.T) {
	accounts, _, _, store := privateCodexAccount(t)
	home, err := accounts.HarnessHome("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "auth.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: store})
	rows, _, err := s.nativeHomeAccounts(context.Background(), false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("probe result: %+v, %v", rows, err)
	}
	if rows[0].Status != "unavailable" || rows[0].NeedsLogin {
		t.Fatalf("probe error requested sign-in: %+v", rows[0])
	}
}

// A failed refresh must say why, not just "could not be refreshed".
func TestProfileModelsNamesWhyTheRefreshFailed(t *testing.T) {
	_, _, _, store := privateCodexAccount(t)
	s := New(Options{Store: store, Models: func(context.Context, string, string, []string) ([]adapters.Model, error) {
		return nil, fmt.Errorf("%w: the harness exited with status 1", adapters.ErrProbe)
	}})
	out, err := s.ProfileModels(context.Background(), "codex", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Message, "could not be refreshed") || !strings.Contains(out.Message, "the harness exited with status 1") {
		t.Fatalf("message = %q; want the failure named", out.Message)
	}
}

// The probe runs the private copy with the same owner tool roots a turn
// gets, so a node shim in PATH (mise) finds the owner state it trusts.
func TestProbeEnvironmentCarriesOwnerToolRoots(t *testing.T) {
	_, _, _, store := privateCodexAccount(t)
	owner := os.Getenv("HOME")
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	// ToolRoots keeps explicit empty values, so the MISE_* keys must be unset,
	// not blank, for the owner defaults to apply.
	for _, key := range []string{"MISE_CONFIG_DIR", "MISE_CACHE_DIR", "MISE_DATA_DIR"} {
		value, had := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if had {
				os.Setenv(key, value)
			} else {
				os.Unsetenv(key)
			}
		})
	}
	var env []string
	s := New(Options{Store: store, Models: func(_ context.Context, _ string, _ string, e []string) ([]adapters.Model, error) {
		env = e
		return []adapters.Model{{ID: "gpt-fixture"}}, nil
	}})
	if _, err := s.ProfileModels(context.Background(), "codex", true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"MISE_CONFIG_DIR=" + filepath.Join(owner, ".config", "mise"),
		"MISE_CACHE_DIR=" + filepath.Join(owner, ".cache", "mise"),
		"MISE_DATA_DIR=" + filepath.Join(owner, ".local", "share", "mise"),
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("probe env lacks %q; got %v", want, env)
		}
	}
}
