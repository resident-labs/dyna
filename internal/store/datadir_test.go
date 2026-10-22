package store_test

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

// A process that lost the owner's environment inside a harness resolves the
// data folder into the runtime home; DataDir refuses it instead of starting
// a second, partial store there.
func TestDataDirRefusesAHarnessRuntimeHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "accounts", "claude-code", ".runtime.home")
	for name, env := range map[string]map[string]string{
		"private XDG_DATA_HOME": {"XDG_DATA_HOME": filepath.Join(home, ".local", "share")},
		"private HOME":          {"XDG_DATA_HOME": "", "HOME": home},
	} {
		t.Run(name, func(t *testing.T) {
			for key, value := range env {
				t.Setenv(key, value)
			}
			err := store.CheckDataDir()
			if err == nil || !strings.Contains(err.Error(), "harness runtime home") || !strings.Contains(err.Error(), "DYNA_OWNER_ENV") {
				t.Fatalf("CheckDataDir = %v, want a runtime home error that names DYNA_OWNER_ENV", err)
			}
			defer func() {
				if recover() == nil {
					t.Fatal("DataDir returned a folder inside a runtime home")
				}
			}()
			store.DataDir()
		})
	}
}

func TestStoretestDataDirIsTheResolvedDataFolder(t *testing.T) {
	want := storetest.DataDir(t)
	if err := store.CheckDataDir(); err != nil {
		t.Fatal(err)
	}
	if got := store.DataDir(); got != want {
		t.Fatalf("DataDir = %s, want %s", got, want)
	}
}
