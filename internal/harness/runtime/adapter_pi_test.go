package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// piTestHome returns a temporary home and points HOME at it, so no test can
// read or write the owner's real home.
func piTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func piTestAdapter(t *testing.T) RuntimeAdapter {
	t.Helper()
	a, err := Adapter("pi")
	if err != nil {
		t.Fatalf("Adapter(pi) failed: %v", err)
	}
	return a
}

func TestPiAdapterRegistration(t *testing.T) {
	a := piTestAdapter(t)
	if a.Harness != "pi" || a.Binary != "pi" {
		t.Fatalf("Harness/Binary = %q/%q", a.Harness, a.Binary)
	}
	if a.NPMPackage != "@earendil-works/pi-coding-agent" {
		t.Fatalf("NPMPackage = %q", a.NPMPackage)
	}
	if strings.Contains(a.NPMPackage, "mariozechner") {
		t.Fatal("the stale @mariozechner package name must never appear")
	}
	if a.ConfigFormat != "json" || a.MCPKey != "mcpServers" {
		t.Fatalf("ConfigFormat/MCPKey = %q/%q", a.ConfigFormat, a.MCPKey)
	}
	if a.ConfigFile != "settings.json" {
		t.Fatalf("ConfigFile = %q", a.ConfigFile)
	}
	if !reflect.DeepEqual(a.AuthFiles, []string{"auth.json"}) {
		t.Fatalf("AuthFiles = %q", a.AuthFiles)
	}
	if len(a.PersonalConfigFiles) != 0 || len(a.ConfigAllowlist) != 0 || len(a.PartialReasons) != 0 {
		t.Fatalf("Pi runs with its defaults: personal files %q, allowlist %q, reasons %q", a.PersonalConfigFiles, a.ConfigAllowlist, a.PartialReasons)
	}
	if a.AccountBlocked != "" {
		t.Fatalf("Pi sign-in writes a private auth.json, so accounts are not blocked: %q", a.AccountBlocked)
	}
	if a.Exclusive {
		t.Fatal("Pi shares the account lease; an exclusive lease queues every Pi turn behind the one running")
	}
	if a.NativeEnv == nil || a.ConfigArgs == nil || a.LoginArgs == nil {
		t.Fatal("NativeEnv, ConfigArgs, and LoginArgs must all be set")
	}
	found := false
	for _, other := range RuntimeAdapters() {
		if other.Harness == "pi" {
			found = true
		}
	}
	if !found {
		t.Fatal("RuntimeAdapters() must list pi")
	}
}

func TestPiNativeEnvStaysPrivate(t *testing.T) {
	a := piTestAdapter(t)
	home := piTestHome(t)
	env := a.NativeEnv(home)
	if env["PI_CODING_AGENT_DIR"] != home {
		t.Fatalf("PI_CODING_AGENT_DIR = %q", env["PI_CODING_AGENT_DIR"])
	}
	if env["PI_CODING_AGENT_SESSION_DIR"] != filepath.Join(home, "sessions") {
		t.Fatalf("PI_CODING_AGENT_SESSION_DIR = %q", env["PI_CODING_AGENT_SESSION_DIR"])
	}
	if env["PI_OFFLINE"] != "1" || env["PI_SKIP_VERSION_CHECK"] != "1" {
		t.Fatal("update and resource checks must be off in the private scope")
	}
	for key, value := range env {
		if key == "HOME" || strings.HasPrefix(key, "XDG_") {
			t.Fatalf("env key %q must not override HOME or XDG roots", key)
		}
		if value == "" || value == "1" {
			continue
		}
		if !strings.HasPrefix(value, home) {
			t.Fatalf("env %s=%q leaves the private home", key, value)
		}
	}
	for _, key := range piProviderEnv {
		if value, ok := env[key]; !ok || value != "" {
			t.Fatalf("inherited provider env %s must be cleared", key)
		}
	}
}

func TestPiConfigArgsStaysPrivate(t *testing.T) {
	a := piTestAdapter(t)
	home := piTestHome(t)
	args := a.ConfigArgs(home, filepath.Join(home, "settings.json"))
	want := []string{"--session-dir", filepath.Join(home, "sessions")}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ConfigArgs = %q, want %q", args, want)
	}
	for _, arg := range args {
		if arg == "HOME" || strings.HasPrefix(arg, "XDG_") {
			t.Fatalf("ConfigArgs escapes private scope: %q", args)
		}
	}
	if !strings.HasPrefix(args[1], home) {
		t.Fatalf("session dir %q leaves the private home", args[1])
	}
}

func TestPiLoginArgsRunInteractiveLogin(t *testing.T) {
	a := piTestAdapter(t)
	home := piTestHome(t)
	executable := filepath.Join(home, "harness", "pi")
	args := a.LoginArgs(executable, home)
	if !reflect.DeepEqual(args, []string{executable}) {
		t.Fatalf("LoginArgs = %q; Pi signs in through interactive /login only", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "=") {
			t.Fatalf("LoginArgs must carry no environment or credentials: %q", arg)
		}
	}
}

func TestPiDeclarativePathsAreRelative(t *testing.T) {
	a := piTestAdapter(t)
	paths := append([]string{a.ConfigFile}, a.AuthFiles...)
	for _, path := range paths {
		if path == "" || filepath.IsAbs(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "~") {
			t.Fatalf("declarative path %q must stay relative and clean", path)
		}
	}
}

func TestPiInstallUsesApprovedRecipe(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if !reflect.DeepEqual(args, []string{"npm", "install", "--global", "--prefix", args[4], "--package-lock", "--ignore-scripts", piNPMPackage + "@1.3.0-dyna.2"}) {
			t.Fatalf("install argv = %q", args)
		}
		return fixtureInstall(ctx, args, env)
	}
	b := install(t, m, "pi", "1.3.0-dyna.2")
	raw, err := os.ReadFile(filepath.Join(b.Directory, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Package != piNPMPackage || manifest.Integrity != "sha512-Zml4dHVyZQ==" {
		t.Fatal("lost exact package integrity", manifest)
	}
}

func TestPiInstallRejectsLooseVersionsAndBadBundles(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	ran := false
	m.Run = func(context.Context, []string, []string) error { ran = true; return nil }
	for _, version := range []string{"", "latest", "1.2", "^1.3.0", "1.3.0;rm -rf /", "1.3.0 --registry=https://evil.example"} {
		if _, err := m.Install(context.Background(), "pi", version); err == nil {
			t.Fatalf("accepted version %q", version)
		}
	}
	if ran {
		t.Fatal("rejected versions reached installer")
	}
	m.Run = func(context.Context, []string, []string) error { return errors.New("npm exploded: secret-token-value") }
	if _, err := m.Install(context.Background(), "pi", "1.3.0-dyna.2"); err == nil || strings.Contains(err.Error(), "secret-token-value") {
		t.Fatal("installer error was lost or exposed", err)
	}
	m.Run = func(context.Context, []string, []string) error { return nil }
	if _, err := m.Install(context.Background(), "pi", "1.3.0-dyna.2"); err == nil {
		t.Fatal("accepted missing executable")
	}
}

func TestPiInstallRecordsPackageIntegrityFallback(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		root := ""
		for i, arg := range args {
			if arg == "--prefix" {
				root = args[i+1]
			}
		}
		if root == "" {
			return errors.New("missing install prefix")
		}
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "bin/pi"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			return err
		}
		dir := filepath.Join(root, "lib/node_modules", piNPMPackage)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"1.3.0-dyna.2","_integrity":"sha512-Zml4dHVyZQ=="}`), 0600)
	}

	b := install(t, m, "pi", "1.3.0-dyna.2")
	raw, _ := os.ReadFile(filepath.Join(b.Directory, "bundle.json"))
	var manifest bundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Integrity != "sha512-Zml4dHVyZQ==" {
		t.Fatal("Pi bypassed shared integrity lookup", manifest.Integrity)
	}
}
