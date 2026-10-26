package engine

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// checkPrivateRuntime refuses to start an engine that a Dyna session launched on
// the owner's login runtime folder. Such a process inherits a room stamp, and
// the login runtime folder holds the owner's live socket: a scratch engine would
// take it, and the owner's engine could not start while that one runs. The
// installed service and an engine the owner starts from a terminal carry no
// stamp, so both keep the normal runtime folder, including a custom
// XDG_DATA_HOME.
func checkPrivateRuntime(stamped bool, runtimeDir string) error {
	if !stamped {
		return nil
	}
	login := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	if filepath.Clean(runtimeDir) != login {
		return nil
	}
	return fmt.Errorf("this engine was started by a Dyna session and XDG_RUNTIME_DIR is the login runtime folder %s, which holds the owner's engine socket: give it a runtime folder of its own", login)
}

// socketOwnerPath names the file beside the socket that records which data
// folder the engine on that socket serves.
func socketOwnerPath(path string) string { return path + ".data" }

// checkSocketOwner refuses a socket that names another data folder. A second
// engine must never take the socket of an engine that serves different data.
func checkSocketOwner(path, dataDir string) error {
	body, err := os.ReadFile(socketOwnerPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner := strings.TrimSpace(string(body)); owner != "" && filepath.Clean(owner) != filepath.Clean(dataDir) {
		return fmt.Errorf("engine socket %s belongs to the engine on data folder %s, not %s", path, owner, dataDir)
	}
	return nil
}

// writeSocketOwner records the data folder this engine serves beside its socket,
// so a later engine on another folder refuses that socket instead of serving it.
func writeSocketOwner(path, dataDir string) error {
	return os.WriteFile(socketOwnerPath(path), []byte(filepath.Clean(dataDir)+"\n"), 0o600)
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
