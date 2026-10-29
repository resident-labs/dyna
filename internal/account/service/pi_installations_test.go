package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"dyna-agent/internal/account/service/adapters"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// piBundleInstall creates the files a private Pi bundle needs without npm.
func piBundleInstall(_ context.Context, args, _ []string) error {
	root := ""
	for i, arg := range args {
		if arg == "--prefix" {
			root = args[i+1]
		}
	}
	const pkg, name = "@earendil-works/pi-coding-agent", "pi"
	version := strings.TrimPrefix(args[len(args)-1], pkg+"@")
	dir := filepath.Join(root, "lib", "node_modules", pkg)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cli.js"), []byte("#!/bin/sh\necho "+version+"\n"), 0700); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join("..", "lib", "node_modules", pkg, "cli.js"), filepath.Join(root, "bin", name)); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]string{"name": pkg, "version": version})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), metadata, 0600); err != nil {
		return err
	}
	lock, _ := json.Marshal(piLockFile{Packages: map[string]piLockPackage{
		"node_modules/" + pkg: {Version: version, Integrity: "sha512-Zml4dHVyZQ=="},
	}})
	return os.WriteFile(filepath.Join(root, "package-lock.json"), lock, 0600)
}

type piLockPackage struct {
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
}

type piLockFile struct {
	Packages map[string]piLockPackage `json:"packages"`
}

func TestPrivateInstallationsResolveEachPiProvider(t *testing.T) {
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
	bundles := &runtime.BundleManager{Root: filepath.Join(root, "harnesses"), Run: piBundleInstall}
	if _, err := bundles.Install(context.Background(), "pi", "1.5.0-dyna.10"); err != nil {
		t.Fatal(err)
	}
	st := &accountstore.Store{Root: filepath.Join(root, "accounts")}
	accounts := &runtime.Accounts{Root: st.Root}
	auth := map[accountstore.Provider]string{
		accountstore.ProviderZAI:      `{"zai":{"type":"api_key","key":"fixture-zai"}}`,
		accountstore.ProviderDeepSeek: `{"deepseek":{"type":"api_key","key":"fixture-deepseek"}}`,
	}
	want := []string{}
	for _, provider := range []accountstore.Provider{accountstore.ProviderZAI, accountstore.ProviderDeepSeek} {
		account, err := accounts.CreateProvider("pi", provider, accountstore.KindAPIKey, string(provider))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(auth[provider]), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := accounts.Capture(context.Background(), account.ID, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.SetActive(provider, "pi", "", account.ID); err != nil {
			t.Fatal(err)
		}
		want = append(want, account.ID)
	}
	got := []string{}
	for _, installation := range New(Options{Store: st}).privateInstallations() {
		if installation.Harness == "pi" {
			got = append(got, installation.AccountID)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("Pi installations = %v, want %v", got, want)
	}
}

func TestProfileModelsMergesEachPiProviderCatalog(t *testing.T) {
	calls := []string{}
	s := New(Options{Models: func(_ context.Context, _, executable string, _ []string) ([]adapters.Model, error) {
		calls = append(calls, executable)
		return []adapters.Model{{ID: "model-" + strings.ReplaceAll(executable, "/", "-")}}, nil
	}})
	s.installed = func() []Installation {
		return []Installation{
			{Harness: "pi", Executable: "/zai/bin/pi", AccountID: "zai-account"},
			{Harness: "pi", Executable: "/deepseek/bin/pi", AccountID: "deepseek-account"},
		}
	}
	catalog, err := s.ProfileModels(context.Background(), "pi", false)
	if err != nil || len(calls) != 2 || len(catalog.Models) != 2 {
		t.Fatalf("catalog = %+v, calls = %v, err = %v", catalog, calls, err)
	}
}
