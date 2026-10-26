package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dyna-agent/internal/fsutil"
)

// A startup the engine socket rejects must leave every published scope link on
// the running engine's build; a startup that owns the listeners repoints them
// at its own. The refresh runs only after listener ownership (D-161).
func TestClaimEngineListenersRefreshesOnlyOwnedStartup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux image cache")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	first := filepath.Join(t.TempDir(), "dyna-first")
	if err := os.WriteFile(first, []byte("#!/bin/sh\n# first build\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	image, err := fsutil.StableImage(first)
	if err != nil {
		t.Fatal(err)
	}
	link, err := fsutil.CurrentLink("/owner/accounts/one/.runtime.home")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsutil.SetLink(link, image); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(t.TempDir(), "engine.sock")
	options := Options{Addr: "127.0.0.1:0", CardAddr: "127.0.0.1:0"}

	running, err := claimEngineListeners(socketPath, t.TempDir(), options, true)
	if err != nil {
		t.Fatal(err)
	}
	defer running.close()
	current, err := fsutil.StableExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(link); err != nil || target != current {
		t.Fatalf("owned startup kept the first build: %q, %v", target, err)
	}

	// The second engine dials the live socket and is rejected. It must not
	// move the link onto its own build before that rejection.
	if _, err := claimEngineListeners(socketPath, t.TempDir(), options, true); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second engine was not rejected: %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != current {
		t.Fatalf("rejected startup moved the scope link: %q, %v", target, err)
	}
}
