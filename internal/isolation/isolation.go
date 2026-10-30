// Package isolation identifies copies of the owner's state.
package isolation

import (
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
	return database.CopyTime(dataDir)
}
