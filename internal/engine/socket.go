package engine

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"dyna-agent/internal/store"
)

// checkPrivateRuntime refuses to start when this engine's data folder is not the
// owner's but its runtime folder is the owner's login runtime folder. The socket
// would then be the owner's live socket, so a scratch or test engine would hold
// it and the real engine could not start while it runs.
func checkPrivateRuntime(dataDir, runtimeDir string) error {
	owner := store.OwnerDataDir()
	if owner == "" || dataDir == "" || dataDir == owner {
		return nil
	}
	login := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	if filepath.Clean(runtimeDir) != login {
		return nil
	}
	return fmt.Errorf("engine data folder %s is not the owner's %s, but XDG_RUNTIME_DIR is the login runtime folder %s: give this engine a runtime folder of its own, so it cannot take the owner's socket", dataDir, owner, login)
}

// listenSocket never replaces an active listener or a non-socket file.
func listenSocket(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("engine socket directory is not a directory: %s", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("engine socket path is not a socket: %s", path)
		}
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil, errors.New("Dyna engine is already running")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}
