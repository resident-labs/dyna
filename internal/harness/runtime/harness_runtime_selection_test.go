package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateRuntimeSnapshotReportsSelectionAndRecommendations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := testPrivateRuntime(t)
	m := NewBundleManager()
	payload := []byte("#!/bin/sh\nexit 0\n")
	sum := sha256.Sum256(payload)
	for _, version := range []string{"1.0.0", "2.0.0"} {
		dir := filepath.Join(m.Root, "codex", version)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "codex"), payload, 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"harness": "codex", "version": version, "executable": "codex", "sha256": hex.EncodeToString(sum[:])})
		if err := os.WriteFile(filepath.Join(dir, "bundle.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Select("codex", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	a.privateRecommendations = func(context.Context) []RecommendedVersion {
		return []RecommendedVersion{{Harness: "codex", Version: "3.0.0", Status: "resolved", Source: "https://example.invalid/fixture"}}
	}
	snapshot, err := a.PrivateRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SelectedVersions["codex"] != "1.0.0" || len(snapshot.Bundles) != 2 {
		t.Fatal("explicit selection was lost", snapshot)
	}
	if len(snapshot.RecommendedVersions) != 1 || snapshot.RecommendedVersions[0].Version != "3.0.0" {
		t.Fatal("recommendation was lost")
	}
	if err := os.Remove(filepath.Join(m.Root, ".codex-selected")); err != nil {
		t.Fatal(err)
	}
	snapshot, err = a.PrivateRuntimes(context.Background())
	if err != nil || snapshot.SelectedVersions["codex"] != "2.0.0" {
		t.Fatal("newest fallback was lost", err)
	}
}
