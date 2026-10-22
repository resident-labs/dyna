package computer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/nodedeps"
	"dyna-agent/internal/store"
)

// browserDeps pins playwright-core. nodedeps installs it once into the Dyna data folder.
//
//go:embed browserdeps/package.json browserdeps/package-lock.json
var browserDeps embed.FS

// installPlaywright runs the install command. Tests replace it; nil runs npm.
var installPlaywright nodedeps.Executor

// playwrightModule installs playwright-core when needed and returns the path of its ESM entry file.
// The snippet imports this absolute path, so no NODE_PATH is needed and it works in CommonJS and ESM scripts.
// A file lock serializes installs. An install that is incomplete or older than the embedded lock file is removed and redone.
func playwrightModule(ctx context.Context) (string, error) {
	dir := filepath.Join(store.DataDir(), "browser", "deps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	unlock, err := store.LockContext(ctx, filepath.Join(dir, ".install.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	var lockSum [32]byte
	for _, name := range []string{"package.json", "package-lock.json"} {
		b, err := browserDeps.ReadFile("browserdeps/" + name)
		if err != nil {
			return "", err
		}
		if name == "package-lock.json" {
			lockSum = sha256.Sum256(b)
		}
		// Rewrite only on change: callers wait on the lock, and a synced write each costs real time on a slow disk.
		if have, err := os.ReadFile(filepath.Join(dir, name)); err == nil && bytes.Equal(have, b) {
			continue
		}
		if err := fsutil.WriteFile(filepath.Join(dir, name), b, 0o644, true); err != nil {
			return "", err
		}
	}
	module := filepath.Join(dir, "node_modules", "playwright-core", "index.mjs")
	stamp, _ := os.ReadFile(filepath.Join(dir, "node_modules", ".dyna-lock-sha256"))
	if _, err := os.Stat(module); err != nil || strings.TrimSpace(string(stamp)) != hex.EncodeToString(lockSum[:]) {
		if err := os.RemoveAll(filepath.Join(dir, "node_modules")); err != nil {
			return "", err
		}
	}
	prepare := nodedeps.Prepare
	if installPlaywright != nil {
		prepare = func(ctx context.Context, dir string) (string, error) {
			return nodedeps.PrepareWithExecutor(ctx, dir, installPlaywright)
		}
	}
	if _, err := prepare(ctx, dir); err != nil {
		return "", fmt.Errorf("install playwright-core: %w", err)
	}
	if _, err := os.Stat(module); err != nil {
		return "", fmt.Errorf("install playwright-core: %w", err)
	}
	return module, nil
}
