// Package fakeruntime lets tests launch fake harness CLIs as Dyna-managed
// copies. It is for tests only: it imports testing, so production code must
// never import it.
package fakeruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	harnesspath "dyna-agent/internal/harness/path"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// Prepare matches harness.PrepareRuntime. It uses the harness binary that the
// test put on PATH and skips private bundles, accounts and generations.
func Prepare(_ context.Context, p profile.Profile, _ *runtime.Pin) (profile.Profile, runtime.Pin, func(), error) {
	def, _ := profile.HarnessDefinitionFor(p.Harness)
	env := os.Environ()
	if path, ok := p.Env["PATH"]; ok {
		env = append(env, "PATH="+path)
	}
	executable, err := harnesspath.Resolve(def.Binary, env)
	if err != nil {
		return p, runtime.Pin{}, nil, err
	}
	p.RuntimeExecutable = executable
	p.RuntimeBindProcess = func(json.RawMessage) error { return nil }
	h, _ := runtime.CanonicalHarness(p.Harness)
	return p, runtime.Pin{Bundle: runtime.Bundle{Harness: h, Executable: executable}}, func() {}, nil
}

// InstallClaude installs a Dyna-managed Claude Code copy that runs command, and
// signs in one fixture account. dataDir is the Dyna data directory of the
// process under test. Cleanup makes the read-only copy removable again.
func InstallClaude(t testing.TB, dataDir, command string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	if err := installClaude(context.Background(), dataDir, command); err != nil {
		t.Fatal(err)
	}
}

func installClaude(ctx context.Context, dataDir, command string) error {
	bundles := &runtime.BundleManager{Root: filepath.Join(dataDir, "harnesses"), Run: func(_ context.Context, args, _ []string) error {
		prefix := args[4]
		pkg := filepath.Join(prefix, "lib", "node_modules", "@anthropic-ai", "claude-code")
		if err := os.MkdirAll(pkg, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pkg, "cli.js"), []byte("#!/bin/sh\nexec "+strconv.Quote(command)+" \"$@\"\n"), 0700); err != nil {
			return err
		}
		if err := os.Symlink("../lib/node_modules/@anthropic-ai/claude-code/cli.js", filepath.Join(prefix, "bin", "claude")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@anthropic-ai/claude-code","version":"1.0.0"}`), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(prefix, "package-lock.json"), []byte(`{"packages":{"node_modules/@anthropic-ai/claude-code":{"version":"1.0.0","integrity":"sha512-Zml4dHVyZQ=="}}}`), 0600)
	}}
	bundle, err := bundles.Install(ctx, "claude-code", "1.0.0")
	if err != nil {
		return err
	}
	accounts := &runtime.Accounts{Root: filepath.Join(dataDir, "accounts")}
	account, err := accounts.Create("claude-code", "fixture")
	if err != nil {
		return err
	}
	for name, raw := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":1999999999999,"scopes":["user:inference"]}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","emailAddress":"fixture@example.test"}}`,
	} {
		if err := os.WriteFile(filepath.Join(account.Home, name), []byte(raw), 0600); err != nil {
			return err
		}
	}
	if _, err := accounts.Capture(ctx, account.ID, bundle.Executable); err != nil {
		return err
	}
	return accounts.Switch(ctx, account.ID, bundle.Executable)
}
