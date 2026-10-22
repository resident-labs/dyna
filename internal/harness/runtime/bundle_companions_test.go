package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/fsutil"
)

var antigravityCompanions = []string{"agy_acp_server.par", "localharness_external"}

// publishAntigravityBundle publishes a bundle directory the manager would
// otherwise reuse, without downloading anything.
func publishAntigravityBundle(t *testing.T, m *BundleManager, version string, companions ...string) Bundle {
	t.Helper()
	dir := filepath.Join(m.Root, "antigravity", version)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join("bin", "agy")
	for _, name := range append([]string{"agy"}, companions...) {
		if err := fsutil.WriteExecutable(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := executableHash(dir, executable)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(bundleManifest{
		Harness: "antigravity", Version: version, Executable: executable, SHA256: hash,
		Package: "fixture", Integrity: "sha256-fixture", Platform: "linux/amd64",
	})
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	return Bundle{Harness: "antigravity", Version: version, Executable: filepath.Join(dir, executable), Directory: dir}
}

// The ACP server and its local harness are part of the Antigravity runtime.
// A published bundle without them must never resolve, be selected, be rolled
// back to or be reused: ACP is the only supported Antigravity path.
func TestBundleRefusesAntigravityWithoutACPCompanions(t *testing.T) {
	for _, missing := range antigravityCompanions {
		t.Run(missing, func(t *testing.T) {
			m := manager(t)
			kept := []string{}
			for _, name := range antigravityCompanions {
				if name != missing {
					kept = append(kept, name)
				}
			}
			publishAntigravityBundle(t, m, "1.4.5", kept...)
			if _, err := m.Resolve("antigravity", "1.4.5"); !errors.Is(err, errBundleUnusable) || !strings.Contains(err.Error(), missing) {
				t.Fatalf("an incomplete bundle resolved: %v", err)
			}
			if _, err := m.Resolve("antigravity", ""); err == nil {
				t.Fatal("an incomplete bundle became the default")
			}
			if err := m.Select("antigravity", "1.4.5"); !errors.Is(err, errBundleUnusable) {
				t.Fatalf("an incomplete bundle was selected: %v", err)
			}
			if err := m.writeMarker(".antigravity-lastgood", "1.4.5"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Rollback(context.Background(), "antigravity"); !errors.Is(err, errBundleUnusable) {
				t.Fatalf("rollback chose an incomplete bundle: %v", err)
			}
			// The harness page still loads; it shows Antigravity as not installed.
			if bundles, err := m.List(); err != nil || len(bundles) != 0 {
				t.Fatalf("List did not skip the incomplete bundle: %v %v", bundles, err)
			}
		})
	}
}

// Install does not reuse an incomplete bundle. It replaces it with a complete
// one, unless an open session still holds it.
func TestBundleInstallDoesNotReuseAntigravityWithoutACPCompanions(t *testing.T) {
	m := manager(t)
	b := publishAntigravityBundle(t, m, "1.4.5", antigravityCompanions...)
	release, err := m.Lease(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Directory, "bin", "agy_acp_server.par")); err != nil {
		t.Fatal(err)
	}
	_, err = m.Install(context.Background(), "antigravity", "1.4.5")
	if !errors.Is(err, errBundleUnusable) || !strings.Contains(err.Error(), "open session") {
		t.Fatalf("a leased incomplete bundle was reused or removed: %v", err)
	}
	if _, err := os.Stat(b.Directory); err != nil {
		t.Fatalf("a leased bundle was removed: %v", err)
	}
	release()
	// No session holds it now, so Install removes it and installs again. The
	// fixture has no Antigravity download, so only the removal is checked.
	_, err = m.Install(context.Background(), "antigravity", "1.4.5")
	if errors.Is(err, errBundleUnusable) {
		t.Fatalf("Install reused the incomplete bundle: %v", err)
	}
	if _, err := os.Stat(b.Directory); !os.IsNotExist(err) {
		t.Fatalf("the incomplete bundle was kept: %v", err)
	}
}

func TestBundleInstallReusesAntigravityWithACPCompanions(t *testing.T) {
	m := manager(t)
	want := publishAntigravityBundle(t, m, "1.4.5", antigravityCompanions...)
	got, err := m.Install(context.Background(), "antigravity", "1.4.5")
	if err != nil || got != want {
		t.Fatalf("a complete bundle was refused: %+v %v", got, err)
	}
	if bundles, err := m.List(); err != nil || len(bundles) != 1 {
		t.Fatalf("a complete bundle left the inventory: %v %v", bundles, err)
	}
}
