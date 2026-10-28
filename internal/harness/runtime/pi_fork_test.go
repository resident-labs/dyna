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

func TestForkInstallAndRollbackPreservesNPMDefault(t *testing.T) {
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
	b, err := m.InstallAndSelect(context.Background(), "pi", version)
	if err != nil || b.Version != version {
		t.Fatalf("fork bundle = %+v, error = %v", b, err)
	}
	rolled, err := m.Rollback(context.Background(), "pi")
	if err != nil || rolled.Version != old.Version {
		t.Fatalf("rollback = %+v, %v", rolled, err)
	}
	selected, installedAgain, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil || installedAgain || selected.Version != old.Version {
		t.Fatalf("default after rollback = %+v, installed = %v, error = %v", selected, installedAgain, err)
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

func TestPiForkStampedLocalIntegrity(t *testing.T) {
	piTestHome(t)
	t.Setenv(piForkTarballEnv, "")
	path := filepath.Join(t.TempDir(), "fork.tgz")
	data := []byte("fixture")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	oldURL, oldAsset, oldIntegrity := piForkURL, piForkAsset, piForkIntegrity
	t.Cleanup(func() { piForkURL, piForkAsset, piForkIntegrity = oldURL, oldAsset, oldIntegrity })
	sum := sha512.Sum512(data)
	piForkURL, piForkAsset, piForkIntegrity = "", path, "sha512-"+base64.StdEncoding.EncodeToString(sum[:])
	m := manager(t)
	t.Setenv(piForkTarballEnv, "")
	a, _ := Adapter("pi")
	m.Run = func(_ context.Context, args, env []string) error {
		dir := filepath.Join(args[4], "lib/node_modules", piNPMPackage)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"1.4.0-dyna.1","dynaFork":1}`), 0600)
	}
	if _, err := installPiForkNPM(context.Background(), m, t.TempDir(), nil, a); err != nil {
		t.Fatal(err)
	}
	piForkIntegrity = "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	if _, err := installPiForkNPM(context.Background(), m, t.TempDir(), nil, a); err == nil {
		t.Fatal("accepted wrong integrity")
	}
}
