package enginesocket

import (
	"os"
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

// The owner's identity comes from the password database, not from HOME: the same
// runtime and data folders give the same socket whoever started the process, and
// a scratch HOME cannot claim the owner's socket.
func TestSocketPathIgnoresAChangedHome(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	owner := OwnerDataDir()
	want, err := PathFor(runtime, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{t.TempDir(), "/tmp/scratch-home"} {
		t.Setenv("HOME", home)
		if OwnerDataDir() != owner {
			t.Fatalf("HOME=%q changed the owner's data folder to %s", home, OwnerDataDir())
		}
		if got, err := PathFor(runtime, owner); err != nil || got != want {
			t.Fatalf("HOME=%q moved the owner's socket to %s, %v; want %s", home, got, err, want)
		}
		// A scratch HOME with no explicit data folder must not reach the owner's
		// plain socket path.
		previous, set := os.LookupEnv("XDG_DATA_HOME")
		if err := os.Unsetenv("XDG_DATA_HOME"); err != nil {
			t.Fatal(err)
		}
		got, err := Path()
		if set {
			t.Setenv("XDG_DATA_HOME", previous)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got == want && filepath.Clean(DataDir()) != filepath.Clean(owner) {
			t.Fatalf("HOME=%q let data folder %s take the owner's socket", home, DataDir())
		}
	}
	// The same explicit data folder reaches the same socket from either HOME.
	data := filepath.Join(t.TempDir(), "shared-data")
	first, _ := PathFor(runtime, data)
	t.Setenv("HOME", t.TempDir())
	second, _ := PathFor(runtime, data)
	if first != second {
		t.Fatalf("one data folder gave two sockets: %s, %s", first, second)
	}
}
