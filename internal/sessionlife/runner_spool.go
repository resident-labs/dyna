package sessionlife

import (
	"context"
	"os"
	"path/filepath"

	"dyna-agent/internal/store"
)

// RunnerSpoolDir is the folder of the turn runners' spool files in a data
// folder: the writes a runner could not send, one file per session, which
// the engine applies (engine/runner_spool.go). The maintenance pass prunes
// the rejected lines it keeps there.
func RunnerSpoolDir(dataDir string) string { return filepath.Join(dataDir, "runner-spool") }

// LockRunnerSpool takes session id's spool lock in dir. The runner holds it
// around its appends and the engine around taking the file, so a rename
// never falls between an append's open and its write.
func LockRunnerSpool(ctx context.Context, dir, id string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return store.LockContext(ctx, filepath.Join(dir, id+".lock"))
}
