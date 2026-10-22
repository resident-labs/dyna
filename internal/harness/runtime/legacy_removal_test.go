package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	harnessmgr "dyna-agent/internal/harness/config"
)

func TestGenerationRequiresPrivateHomeMarker(t *testing.T) {
	dir := t.TempDir()
	if _, err := restoreGenerationHome("grok", dir, harnessmgr.ConfigGeneration{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation without private HOME marker: %v; want missing-file error", err)
	}
}

func TestNativeGenerationRejectsVersionOne(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	if err := writeNativeGeneration(filepath.Join(dir, "native-generation.json"), nativeGeneration{Version: 1, Harness: "grok", Home: home}); err != nil {
		t.Fatal(err)
	}
	if err := writeNativeGeneration(filepath.Join(dir, "private-home.json"), privateGenerationHome{Version: 1, Home: home}); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeGeneration("grok", dir, nil); err == nil {
		t.Fatal("version 1 native manifest was accepted")
	}
}

func TestGenerationPinsAcceptEquivalentRootAlias(t *testing.T) {
	root := t.TempDir()
	canonical := filepath.Join(root, "generations")
	if err := os.Mkdir(canonical, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Generations: canonical}
	keep := map[string]bool{}
	if err := r.keepGenerationPath(keep, filepath.Join(alias, "generation")); err != nil {
		t.Fatal("generation pin through an equivalent root alias was rejected", err)
	}
	if err := r.keepGenerationPath(keep, filepath.Join(root, "outside", "generation")); err == nil {
		t.Fatal("generation pin outside the root was accepted")
	}
	pin := filepath.Join(canonical, "generation")
	if err := r.keepGenerationPath(keep, pin); err != nil {
		t.Fatal(err)
	}
	if len(keep) != 1 || !keep[pin] {
		t.Fatal("canonical generation pin was not retained")
	}
}
