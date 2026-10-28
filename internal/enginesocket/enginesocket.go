// Package enginesocket names the engine's private Unix socket. It imports
// nothing from Dyna, so both the client and the identity resolver can use it
// without pulling in each other.
package enginesocket

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// Path is the socket the engine listens on and the CLI dials.
//
// The path follows the runtime folder, not the data folder: every process on
// this runtime folder meets the same engine, which is what the CLI relies on.
// An engine on another data folder is refused by the owner record written beside
// the socket (internal/engine), so it can never serve this one's traffic.
// An empty XDG_RUNTIME_DIR uses the conventional /run/user/<uid> folder.
func Path() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	if !filepath.IsAbs(runtime) {
		return "", errors.New("XDG_RUNTIME_DIR must be an absolute path")
	}
	return filepath.Join(runtime, "dyna", "engine.sock"), nil
}
