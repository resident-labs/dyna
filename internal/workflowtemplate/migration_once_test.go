package workflowtemplate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"dyna-agent/internal/database"
)

func TestSeedHashesSurviveStaleManifest(t *testing.T) {
	cat := newSeedCatalog(t, filepath.Join(t.TempDir(), "templates"))
	if _, err := cat.Seed(fstest.MapFS{"echo.js": {Data: []byte("return 1\n")}}); err != nil {
		t.Fatal(err)
	}
	writeOldManifest(t, cat.RegistryDir, map[string]string{"echo": "incorrect"})
	stale, err := os.ReadFile(manifestPath(cat.RegistryDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(cat.store.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(cat.store.DataDir); err != nil {
		t.Fatal(err)
	}
	next := []byte("return 2\n")
	result, err := cat.Seed(fstest.MapFS{"echo.js": {Data: next}})
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("canonical hash did not update starter: %#v, %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(cat.RegistryDir, "echo.js"))
	if err != nil || !bytes.Equal(got, next) {
		t.Fatalf("starter = %q, %v", got, err)
	}
	got, err = os.ReadFile(manifestPath(cat.RegistryDir))
	if err != nil || !bytes.Equal(got, stale) {
		t.Fatalf("retained manifest changed: %q, %v", got, err)
	}
}
