package workflowresolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	root := t.TempDir()
	script, work, registry := filepath.Join(root, "script"), filepath.Join(root, "work"), filepath.Join(root, "registry")
	dirs := []string{script, work, registry, filepath.Join(work, "examples")}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "child.js"), []byte(dir), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range dirs {
		path, src, err := Resolve("child", filepath.Join(script, "parent.js"), work, registry)
		if err != nil || path != filepath.Join(want, "child.js") || src != want {
			t.Fatalf("got %q, %q, %v; want %q", path, src, err, want)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{"child", "", " "} {
		_, _, err := Resolve(ref, "", work, registry)
		var resolution *Error
		if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &resolution) {
			t.Fatalf("%q: %v", ref, err)
		}
	}
}
