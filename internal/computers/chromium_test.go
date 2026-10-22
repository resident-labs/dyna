package computers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserAutomationUsesSharedChromiumLauncher(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	env, err := Environment(Meta{SessionID: "shared-wrapper"}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if envValue(env, "DYNA_CHROMIUM_EXECUTABLE") != envValue(env, "AGENT_BROWSER_EXECUTABLE_PATH") {
		t.Fatal("automation paths differ")
	}
	for _, name := range []string{"chromium", "google-chrome", "google-chrome-stable", "brave", "dyna-chromium"} {
		body, err := os.ReadFile(filepath.Join(Dir("shared-wrapper"), "bin", name))
		if err != nil || !strings.Contains(string(body), " computers chromium 'shared-wrapper' -- ") {
			t.Fatalf("%s bypasses shared launcher: %v", name, err)
		}
	}
	if err := os.WriteFile(chromiumFlagsFile("shared-wrapper"), []byte("--test=two words\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareChromium("shared-wrapper"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(chromiumFlagsFile("shared-wrapper")); string(b) != "--test=two words\n" {
		t.Fatal("overwrote per-computer flags")
	}
}

func TestFindChromiumBinarySkipsInvalidDownloads(t *testing.T) {
	cache := t.TempDir()
	for version, mode := range map[string]os.FileMode{"100": 0700, "200": 0700, "300": 0600} {
		path := filepath.Join(cache, version, "chrome-linux64", "chrome")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(cache, "400", "chrome-linux64", "chrome")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	patterns := []string{filepath.Join(cache, "missing/*/chrome"), filepath.Join(cache, "*/chrome-linux64/chrome")}
	if got := findChromiumBinary(patterns); got != filepath.Join(cache, "200/chrome-linux64/chrome") {
		t.Fatalf("selected %q", got)
	}
	if got := findChromiumBinary(patterns[:1]); got != "" {
		t.Fatalf("missing cache returned %q", got)
	}
}

func TestChromiumBinaryDistributionAndAgentBrowserPaths(t *testing.T) {
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	t.Setenv("PUPPETEER_CACHE_DIR", "")
	for _, relative := range []string{
		"snap/chromium/current/usr/lib/chromium-browser/chrome",
		"home/browser-user/.agent-browser/browsers/chrome-154.0.8037.57/chrome",
		"home/browser-user/.agent-browser/browsers/chrome-154.0.8037.57/chrome-linux64/chrome",
	} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			want := filepath.Join(root, relative)
			if err := os.MkdirAll(filepath.Dir(want), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(want, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			patterns := chromiumBinaryCandidates("chromium", "/home/browser-user")
			for i := range patterns {
				patterns[i] = filepath.Join(root, patterns[i])
			}
			if got := findChromiumBinary(patterns); got != want {
				t.Fatalf("Chromium path = %q, want %q", got, want)
			}
		})
	}
}
