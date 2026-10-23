package engine

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/store/storetest"
)

func TestRuntimeConstructionDoesNotOpenAccountStorage(t *testing.T) {
	dataDir := storetest.DataDir(t)
	newRuntime(t.Context())
	for _, name := range []string{"dyna.db", "accounts"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(err) {
			t.Fatalf("runtime construction opened %s: %v", name, err)
		}
	}
}
