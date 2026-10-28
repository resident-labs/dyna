package runtime

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFirstUseInstallsForkTarballOverNPMDefault(t *testing.T) {
	piTestHome(t)
	m := manager(t)
	old := install(t, m, "pi", "1.4.0")
	payload := []byte("Dyna Pi fork tarball fixture")
	tarball := filepath.Join(t.TempDir(), "pi-fork.tgz")
	if err := os.WriteFile(tarball, payload, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_PI_TARBALL", tarball)

	const version = "1.4.0-dyna.1"
	m.Run = func(ctx context.Context, args, env []string) error {
		want := []string{"npm", "install", "--global", "--prefix", args[4], "--package-lock", "--ignore-scripts", tarball}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("fork install argv = %q, want %q", args, want)
		}
		root := args[4]
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "bin/pi"), []byte("#!/bin/sh\necho "+version+"\n"), 0700); err != nil {
			return err
		}
		dir := filepath.Join(root, "lib/node_modules", piNPMPackage)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		manifest, _ := json.Marshal(struct {
			Name     string `json:"name"`
			Version  string `json:"version"`
			DynaFork int    `json:"dynaFork"`
		}{piNPMPackage, version, 1})
		return os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0600)
	}
	f := &FirstUse{Bundles: m, Latest: func(context.Context, string) (string, error) { return version, nil }}
	b, installed, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil {
		t.Fatal(err)
	}
	if !installed || b.Version != version {
		t.Fatalf("fork bundle = %+v, installed = %v", b, installed)
	}
	pinned, err := m.Resolve("pi", old.Version)
	if err != nil || pinned.Version != old.Version {
		t.Fatalf("existing npm pin was not preserved: %+v, %v", pinned, err)
	}
	raw, err := os.ReadFile(filepath.Join(b.Directory, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512(payload)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	if manifest.Integrity != integrity {
		t.Fatalf("integrity = %q, want %q", manifest.Integrity, integrity)
	}
}
