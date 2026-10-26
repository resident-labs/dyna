package enginesocket

import (
	"path/filepath"
	"strings"
	"testing"
)

// The owner's data folder keeps the historical socket path, and any other data
// folder gets one of its own: a second engine can never take the owner's socket
// even when both use the login runtime folder.
func TestSocketPathFollowsTheDataFolder(t *testing.T) {
	runtime := t.TempDir()
	owner, err := PathFor(runtime, OwnerDataDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(runtime, "dyna", "engine.sock"); owner != want {
		t.Fatalf("owner socket = %s, want %s", owner, want)
	}
	other, err := PathFor(runtime, filepath.Join(t.TempDir(), "data", "dyna"))
	if err != nil {
		t.Fatal(err)
	}
	if other == owner {
		t.Fatal("a second data folder took the owner's socket path")
	}
	again, err := PathFor(runtime, filepath.Join(filepath.Dir(other), "..", "x", "..", "x"))
	if err != nil {
		t.Fatal(err)
	}
	stable, err := PathFor(runtime, filepath.Join(filepath.Dir(other), "..", "x", "..", "x"))
	if err != nil || again != stable {
		t.Fatalf("a data folder's socket path is not stable: %s, %s, %v", again, stable, err)
	}
	same, err := PathFor(runtime, strings.TrimSuffix(other, "/engine.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if same == owner {
		t.Fatal("a derived socket path collapsed to the owner's")
	}
}
