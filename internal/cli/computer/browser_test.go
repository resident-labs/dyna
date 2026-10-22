package computer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/nodedeps"
	"dyna-agent/internal/store"
)

func browserFixture(t *testing.T, handle computers.Handler) func(...string) (string, error) {
	t.Helper()
	rootComputerFixture(t)
	installPlaywright = fakePlaywrightInstall(nil)
	t.Cleanup(func() { installPlaywright = nil })
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	// The fake daemon never launches a desktop. Supply the setup prerequisite
	// without depending on the test host's GUI packages.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	socket := computers.ControlSocket("")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- computers.Serve(ctx, listener, handle) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return func(args ...string) (string, error) {
		cmd := NewBrowserCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
}

func TestBrowserUsesActingComputerAndPrintsWorkingSnippet(t *testing.T) {
	const endpoint = "ws://127.0.0.1:45678/devtools/browser/id"
	var actor string
	call := browserFixture(t, func(_ context.Context, r computers.Request) (any, error) {
		if r.Op == "register" {
			actor = r.Session
			return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
		}
		if actor == "" || r.Session != actor {
			return nil, fmt.Errorf("wrong computer: %q", r.Session)
		}
		switch r.Op {
		case "browser.start":
			return computers.Browser{Endpoint: endpoint}, nil
		case "browser.stop":
			return map[string]bool{"stopped": true}, nil
		}
		return nil, fmt.Errorf("unexpected operation %s", r.Op)
	})
	for i := 0; i < 2; i++ {
		out, err := call()
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 4 || lines[0] != endpoint || !strings.Contains(lines[2], "context.pages().at(-1)") || !strings.Contains(lines[2], "chromium.connectOverCDP(\""+endpoint+"\")") || lines[3] != "const cdp = await context.newCDPSession(page);" {
			t.Fatalf("browser output: %q", out)
		}
	}
	if out, err := call("stop"); err != nil || !strings.Contains(out, `"stopped":true`) {
		t.Fatalf("stop: %q %v", out, err)
	}
	if _, err := call("--session", "other"); err == nil {
		t.Fatal("browser accepted another session")
	}
}

// fakePlaywrightInstall stands in for npm ci: it writes the module entry file. count, when set, counts runs.
func fakePlaywrightInstall(count *int32) nodedeps.Executor {
	return func(_ context.Context, dir string, args []string, _ io.Writer) error {
		if count != nil {
			atomic.AddInt32(count, 1)
		}
		if strings.Join(args, " ") != "npm ci" {
			return fmt.Errorf("install command: %v", args)
		}
		module := filepath.Join(dir, "node_modules", "playwright-core")
		if err := os.MkdirAll(module, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(module, "index.mjs"), []byte("export const chromium = {}\n"), 0o644)
	}
}

func TestBrowserSnippetImportsManagedPlaywright(t *testing.T) {
	var installs int32
	call := browserFixture(t, func(_ context.Context, r computers.Request) (any, error) {
		if r.Op == "register" {
			return computers.Meta{SessionID: r.Session, Room: r.Room}, nil
		}
		return computers.Browser{Endpoint: "ws://127.0.0.1:1/devtools/browser/id"}, nil
	})
	installPlaywright = fakePlaywrightInstall(&installs)
	for i := 0; i < 2; i++ {
		out, err := call()
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Split(out, "\n")[1]
		path := strings.TrimSuffix(strings.TrimPrefix(line, `const { chromium } = await import(`), `);`)
		path, err = strconv.Unquote(path)
		if err != nil {
			t.Fatalf("import line %q: %v", line, err)
		}
		if !strings.HasPrefix(path, filepath.Join(store.DataDir(), "browser")) {
			t.Fatalf("module is outside the managed folder: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("snippet module is missing: %v", err)
		}
	}
	if installs != 1 {
		t.Fatalf("npm ci ran %d times, want 1", installs)
	}
}

func TestPlaywrightModuleRepairsIncompleteInstall(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var installs int32
	installPlaywright = fakePlaywrightInstall(&installs)
	t.Cleanup(func() { installPlaywright = nil })
	module, err := playwrightModule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An interrupted install leaves node_modules without the module.
	if err := os.RemoveAll(filepath.Dir(module)); err != nil {
		t.Fatal(err)
	}
	if module, err = playwrightModule(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(module); err != nil {
		t.Fatalf("module not repaired: %v", err)
	}
	// A stamp from an older lock file means a stale install.
	stamp := filepath.Join(filepath.Dir(filepath.Dir(module)), ".dyna-lock-sha256")
	if err := os.WriteFile(stamp, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := playwrightModule(context.Background()); err != nil {
		t.Fatal(err)
	}
	if installs != 3 {
		t.Fatalf("installs = %d, want 3", installs)
	}
}

func TestPlaywrightModuleInstallsOnceWhenConcurrent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var installs int32
	installPlaywright = fakePlaywrightInstall(&installs)
	t.Cleanup(func() { installPlaywright = nil })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := playwrightModule(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if installs != 1 {
		t.Fatalf("installs = %d, want 1", installs)
	}
}

// A second call must not rewrite the package files it already wrote: every
// rewrite is a synced write under the install lock.
func TestPlaywrightModuleKeepsUnchangedPackageFiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	installPlaywright = fakePlaywrightInstall(nil)
	t.Cleanup(func() { installPlaywright = nil })
	if _, err := playwrightModule(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.DataDir(), "browser", "deps", "package-lock.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := playwrightModule(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged package-lock.json was rewritten")
	}
}
