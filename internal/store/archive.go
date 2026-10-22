package store

import (
	"os"
	"path/filepath"
	"strings"
)

// RunArchiveDirEnv overrides the archive root for retained workflow runs.
const RunArchiveDirEnv = "DYNA_ARCHIVE_DIR"

// RunArchiveDir resolves the configured archive root, using dataDir only for
// the default. Every process resolving archived run IDs must use the same setting.
func RunArchiveDir(dataDir string) string {
	if x := strings.TrimSpace(os.Getenv(RunArchiveDirEnv)); x != "" {
		if abs, err := filepath.Abs(x); err == nil {
			return filepath.Clean(abs)
		}
		return filepath.Clean(x)
	}
	return filepath.Join(dataDir, "archive", "runs")
}
