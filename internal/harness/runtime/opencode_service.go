package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"dyna-agent/internal/account/native"
)

func readOpenCodeServiceConfig(home string) (map[string]json.RawMessage, error) {
	path := filepath.Join(home, "service.json")
	if err := checkPrivateAccountFile(path, false); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var config map[string]json.RawMessage
	if len(raw) > 64<<10 || json.Unmarshal(raw, &config) != nil || config == nil {
		return nil, errors.New("OpenCode private service configuration is invalid.")
	}
	return config, nil
}

func openCodeServicePort(config map[string]json.RawMessage) int {
	var port int
	_ = json.Unmarshal(config["port"], &port)
	return port
}

// Each private scope needs a different service port. OpenCode otherwise uses
// port 49374 across all homes. Preserve its generated password and settings.
func ensureOpenCodeServicePort(home string) error {
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	config, err := readOpenCodeServiceConfig(home)
	if err != nil {
		return err
	}
	if port := openCodeServicePort(config); port > 0 && port <= 65535 {
		return nil
	}
	lock, err := acquireAccountLock(context.Background(), filepath.Join(home, native.OpenCodeServiceLockFile), syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer releaseAccountLock(lock)
	config, err = readOpenCodeServiceConfig(home)
	if err != nil {
		return err
	}
	if port := openCodeServicePort(config); port > 0 && port <= 65535 {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		return err
	}
	config["port"], _ = json.Marshal(port)
	config["hostname"], _ = json.Marshal("127.0.0.1")
	return writePrivateAccountJSON(filepath.Join(home, "service.json"), config)
}
