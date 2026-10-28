package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// forkLayout writes the npm global layout the fork tarball installs, with a
// package manifest at the chosen fork level.
func forkLayout(root, version string, level int) error {
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		return err
	}
	script := "#!/bin/sh\necho " + version + "\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "pi"), []byte(script), 0o700); err != nil {
		return err
	}
	dir := filepath.Join(root, "lib", "node_modules", piNPMPackage)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	manifest, _ := json.Marshal(struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		DynaFork int    `json:"dynaFork"`
	}{piNPMPackage, version, level})
	return os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0o600)
}

func TestFirstUseInstallsForkForUnpinnedTurns(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	const version = piForkVersion
	m.Run = func(_ context.Context, args, _ []string) error {
		want := []string{"npm", "install", "--global", "--prefix", args[4], "--package-lock", "--ignore-scripts", os.Getenv(piForkTarballEnv)}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("fork install argv = %q, want %q", args, want)
		}
		return forkLayout(args[4], version, minimumPiForkLevel)
	}
	f := &FirstUse{Bundles: m}
	b, installed, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil || !installed || b.Version != version {
		t.Fatalf("fork bundle = %+v, installed = %v, %v", b, installed, err)
	}
	selected, installedAgain, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil || installedAgain || selected.Version != version {
		t.Fatalf("resolved = %+v, installed = %v, %v", selected, installedAgain, err)
	}
	pinned, err := m.Resolve("pi", version)
	if err != nil || pinned.Version != version {
		t.Fatalf("existing fork pin was not preserved: %+v, %v", pinned, err)
	}
}

// The fork level in the package manifest is the feature gate: a bundle below
// the level Dyna requires never publishes, one at the level does.
func TestPiForkLevelGate(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	m.Run = func(_ context.Context, args, _ []string) error {
		return forkLayout(args[4], piForkVersion, minimumPiForkLevel-1)
	}
	if _, err := m.Install(context.Background(), "pi", piForkVersion); err == nil || !strings.Contains(err.Error(), "fork level") {
		t.Fatalf("lower fork level accepted: %v", err)
	}
	m.Run = func(_ context.Context, args, _ []string) error {
		return forkLayout(args[4], piForkVersion, minimumPiForkLevel)
	}
	if _, err := m.Install(context.Background(), "pi", piForkVersion); err != nil {
		t.Fatalf("current fork level refused: %v", err)
	}
}

// Upstream npm Pi and older forks are outside the supported range, so
// installing, selecting and rolling back to them all refuse with the reason.
func TestUnsupportedPiVersionsRefused(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	for _, v := range []string{"1.2.9", "1.3.0", "1.4.0", "1.4.0-dyna.1"} {
		if _, err := m.Install(context.Background(), "pi", v); err == nil || !strings.Contains(err.Error(), "Dyna's Pi fork") {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	b := install(t, m, "pi", piForkVersion)
	for _, v := range []string{"1.4.0", "1.4.0-dyna.1"} {
		if err := m.Select("pi", v); err == nil || !strings.Contains(err.Error(), "Dyna's Pi fork") {
			t.Fatalf("select %s: %v", v, err)
		}
	}
	if err := m.writeMarker(".pi-lastgood", "1.4.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rollback(context.Background(), "pi"); err == nil || !strings.Contains(err.Error(), "1.4.0 is not supported") {
		t.Fatalf("rollback to npm: %v", err)
	}
	selected, err := m.Resolve("pi", "")
	if err != nil || selected.Version != b.Version {
		t.Fatalf("refused rollback changed the selection: %+v %v", selected, err)
	}
}

func TestPiForkStampedLocalIntegrity(t *testing.T) {
	piTestHome(t)
	path := filepath.Join(t.TempDir(), "fork.tgz")
	data := []byte("fixture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	oldURL, oldAsset, oldIntegrity := piForkURL, piForkAsset, piForkIntegrity
	t.Cleanup(func() { piForkURL, piForkAsset, piForkIntegrity = oldURL, oldAsset, oldIntegrity })
	sum := sha512.Sum512(data)
	piForkURL, piForkAsset, piForkIntegrity = "", path, "sha512-"+base64.StdEncoding.EncodeToString(sum[:])
	m := manager(t)
	t.Setenv(piForkTarballEnv, "")
	a, _ := Adapter("pi")
	m.Run = func(_ context.Context, args, _ []string) error {
		return forkLayout(args[4], piForkVersion, minimumPiForkLevel)
	}
	artifact, err := installPiForkNPM(context.Background(), m, t.TempDir(), nil, a)
	if err != nil || artifact.Integrity != piForkIntegrity {
		t.Fatalf("artifact = %+v, %v; want the stamped integrity", artifact, err)
	}
	piForkIntegrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	if _, err := installPiForkNPM(context.Background(), m, t.TempDir(), nil, a); err == nil {
		t.Fatal("accepted wrong integrity")
	}
}

// seedBundle writes a published-bundle directory by hand, standing in for an
// old installation that predates the required fork level.
func seedBundle(t *testing.T, root, version string) {
	t.Helper()
	dir := filepath.Join(root, "pi", version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("#!/bin/sh\necho " + version + "\n")
	if err := os.WriteFile(filepath.Join(dir, "pi"), payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	raw, _ := json.Marshal(map[string]string{"harness": "pi", "version": version, "executable": "pi", "sha256": hex.EncodeToString(sum[:])})
	if err := os.WriteFile(filepath.Join(dir, "bundle.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An old version below the required fork level stays visible to cleanup with
// its manifest data, so uninstall and removal can still act on it.
func TestUnusablePiBundleStaysVisibleToCleanup(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	install(t, m, "pi", "1.5.0-dyna.2")
	seedBundle(t, m.Root, "1.4.0")
	bundles, err := m.listHarness("pi", true)
	if err != nil {
		t.Fatal(err)
	}
	var old *Bundle
	for i := range bundles {
		if bundles[i].Version == "1.4.0" {
			old = &bundles[i]
		}
	}
	if old == nil || old.Directory == "" || old.Executable == "" {
		t.Fatalf("unusable bundle lost its manifest data: %+v", old)
	}
	if usable, err := m.listHarness("pi", false); err != nil || len(usable) != 1 {
		t.Fatalf("unusable bundle visible to resolution: %+v %v", usable, err)
	}
	if err := m.Uninstall(context.Background(), "pi", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "pi", "1.4.0")); !os.IsNotExist(err) {
		t.Fatal("uninstall kept an unusable old version")
	}
}

func TestGCRemovesUnusableOldPiBundle(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	install(t, m, "pi", "1.5.0-dyna.2")
	install(t, m, "pi", "1.4.0-dyna.2")
	seedBundle(t, m.Root, "1.0.0")
	if err := m.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "pi", "1.0.0")); !os.IsNotExist(err) {
		t.Fatal("GC kept an unusable old version")
	}
	if _, err := m.Resolve("pi", "1.4.0-dyna.2"); err != nil {
		t.Fatal("GC removed a usable version", err)
	}
}

// A live pin protects an old version like any other: GC keeps it and Remove
// refuses while it is pinned; once unused, individual removal succeeds.
func TestPinnedUnusablePiBundleIsProtected(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	install(t, m, "pi", "1.5.0-dyna.2")
	install(t, m, "pi", "1.4.0-dyna.2")
	seedBundle(t, m.Root, "1.0.0")
	var old Bundle
	bundles, err := m.listHarness("pi", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bundles {
		if b.Version == "1.0.0" {
			old = b
		}
	}
	if old.Directory == "" {
		t.Fatal("unusable bundle lost its manifest data")
	}
	pin := []PinnedBundle{{Bundle: old}}
	if err := m.GC(pin); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.Directory); err != nil {
		t.Fatal("GC removed a pinned unusable bundle", err)
	}
	if err := m.Remove(context.Background(), "pi", "1.0.0", pin, true); err == nil {
		t.Fatal("removed a pinned unusable bundle")
	}
	if err := m.Remove(context.Background(), "pi", "1.0.0", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.Directory); !os.IsNotExist(err) {
		t.Fatal("unused unusable bundle was not removed")
	}
}
