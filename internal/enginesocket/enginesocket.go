// Package enginesocket names the engine's private Unix socket. It imports
// nothing from Dyna, so both the client and the identity resolver can use it
// without pulling in each other.
package enginesocket

import (
	"errors"
	"os"
	"path/filepath"
)

// Path is the socket the engine listens on and the CLI dials.
func Path() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return "", errors.New("XDG_RUNTIME_DIR must be an absolute path")
	}
	return filepath.Join(runtime, "dyna", "engine.sock"), nil
}
