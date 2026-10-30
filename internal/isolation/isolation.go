// Package isolation identifies copies of the owner's state.
package isolation

import (
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

// CopyTime returns the copy time only for a marked database outside the
// installed engine's default data directory.
func CopyTime(dataDir string) (time.Time, error) {
	if filepath.Clean(dataDir) == filepath.Clean(store.OwnerDataDir()) {
		return time.Time{}, nil
	}
	if _, err := os.Stat(filepath.Join(dataDir, "dyna.db")); os.IsNotExist(err) {
		return time.Time{}, nil
	} else if err != nil {
		return time.Time{}, err
	}
	return database.CopyTime(dataDir)
}
