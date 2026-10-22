package storetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/store"
)

func TestClearStampPreservesOtherEnvironment(t *testing.T) {
	for _, key := range mode.StampKeys() {
		t.Setenv(key, "inherited")
	}
	t.Setenv("DYNA_OWNER_ENV", "owner environment")
	t.Setenv("DYNA_BIN", "binary path")
	t.Setenv("DYNA_LAUNCH_GATE", "launch descriptor")
	before := os.Environ()
	ClearStamp(t)
	stamps := map[string]bool{}
	for _, key := range mode.StampKeys() {
		stamps[key] = true
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s remains %q", key, got)
		}
	}
	for _, entry := range before {
		key, value, _ := strings.Cut(entry, "=")
		if !stamps[key] && os.Getenv(key) != value {
			t.Errorf("stamp reset changed %s", key)
		}
	}
}

func TestIsolateOwnsHomesAndRestoresEnvironment(t *testing.T) {
	before := map[string]string{}
	for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "TMPDIR"} {
		before[key] = t.TempDir()
		t.Setenv(key, before[key])
	}
	for _, key := range mode.StampKeys() {
		t.Setenv(key, "inherited")
	}
	t.Run("isolated", func(t *testing.T) {
		root := Isolate(t)
		if got := store.DataDir(); got != root {
			t.Fatalf("data folder = %s; want %s", got, root)
		}
		if !mode.OwnerProcess() {
			t.Fatal("fixture kept an inherited worker identity")
		}
		for _, key := range mode.StampKeys() {
			if os.Getenv(key) != "" {
				t.Errorf("%s was not cleared", key)
			}
		}
		for key, old := range before {
			path := os.Getenv(key)
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() || path == old || !filepath.IsAbs(path) {
				t.Errorf("%s is not a private fixture folder: %q, %v", key, path, err)
			}
		}
	})
	for key, value := range before {
		if os.Getenv(key) != value {
			t.Errorf("%s was not restored", key)
		}
	}
	for _, key := range mode.StampKeys() {
		if os.Getenv(key) != "inherited" {
			t.Errorf("%s was not restored", key)
		}
	}
}
