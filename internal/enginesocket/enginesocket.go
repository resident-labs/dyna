// Package enginesocket names the engine's private Unix socket. It imports
// nothing from Dyna, so both the client and the identity resolver can use it
// without pulling in each other.
package enginesocket

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// Path is the socket the engine listens on and the CLI dials.
func Path() (string, error) {
	return PathFor(os.Getenv("XDG_RUNTIME_DIR"), DataDir())
}

// DataDir is the data folder this process uses, read the same way the engine
// reads it: XDG_DATA_HOME, else HOME/.local/share/dyna.
func DataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "dyna")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "dyna")
}

// OwnerDataDir is the data folder of this user's own engine, which keeps the
// historical socket path so an installed engine and the CLI still meet.
func OwnerDataDir() string {
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "dyna")
}

// PathFor names the socket for one runtime folder and one data folder. The
// owner's data folder keeps the plain path; any other data folder gets one of
// its own, so a second engine can never take the owner's socket.
func PathFor(runtime, dataDir string) (string, error) {
	if !filepath.IsAbs(runtime) {
		return "", errors.New("XDG_RUNTIME_DIR must be an absolute path")
	}
	if dataDir == "" || filepath.Clean(dataDir) == filepath.Clean(OwnerDataDir()) {
		return filepath.Join(runtime, "dyna", "engine.sock"), nil
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return filepath.Join(runtime, "dyna", "engines", hex.EncodeToString(sum[:8]), "engine.sock"), nil
}
