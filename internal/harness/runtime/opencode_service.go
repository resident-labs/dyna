package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/profile"
)

// OpenCode 2.0.32 reads config when serve starts. Native service clients also
// send their process environment to session.environment; --server clients do
// not. Keep that native service path and drain clients before changing config.
type openCodeServiceGeneration struct {
	Generation string
	Executable string
	PID        int
}

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

func openCodeServicePID(env map[string]string) int {
	path := filepath.Join(env["XDG_STATE_HOME"], "opencode", "service.json")
	if checkPrivateAccountFile(path, false) != nil {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return 0
	}
	var registration struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(raw, &registration) != nil {
		return 0
	}
	return registration.PID
}

func prepareOpenCodeService(ctx context.Context, p profile.Profile, pin Pin) (*ResourceLease, error) {
	home := p.Env["OPENCODE_CONFIG_DIR"]
	lock, err := acquireAccountLock(ctx, filepath.Join(home, ".opencode-generation.lock"), syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	defer releaseAccountLock(lock)
	marker := filepath.Join(home, ".dyna-service-generation.json")
	if err = checkPrivateAccountFile(marker, false); err != nil {
		return nil, err
	}
	var previous openCodeServiceGeneration
	raw, readErr := os.ReadFile(marker)
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, readErr
	}
	if len(raw) > 64<<10 || (readErr == nil && json.Unmarshal(raw, &previous) != nil) {
		return nil, errors.New("OpenCode private service generation is invalid.")
	}
	next := openCodeServiceGeneration{Generation: pin.Generation, Executable: pin.Bundle.Executable, PID: openCodeServicePID(p.Env)}
	uses := filepath.Join(home, ".opencode-turns")
	if next.Generation != previous.Generation || next.Executable != previous.Executable || next.PID == 0 || next.PID != previous.PID {
		if err = drainResourceLeases(ctx, uses); err != nil {
			return nil, err
		}
		if err = ensureOpenCodeServicePort(home); err != nil {
			return nil, err
		}
		// Keep account probes available while active clients drain. Only the
		// restart itself excludes native account commands.
		commandLock, err := acquireAccountLock(ctx, filepath.Join(home, native.OpenCodeServiceLockFile), syscall.LOCK_EX)
		if err != nil {
			return nil, err
		}
		defer releaseAccountLock(commandLock)
		config, err := readOpenCodeServiceConfig(home)
		if err != nil {
			return nil, err
		}
		// Persist the config path, not resolved tokens from the turn environment.
		var serviceEnv map[string]string
		if raw := config["env"]; len(raw) != 0 && json.Unmarshal(raw, &serviceEnv) != nil {
			return nil, errors.New("OpenCode private service environment is invalid.")
		}
		if serviceEnv == nil {
			serviceEnv = map[string]string{}
		}
		serviceEnv["OPENCODE_CONFIG"] = p.Env["OPENCODE_CONFIG"]
		config["env"], _ = json.Marshal(serviceEnv)
		if err = writePrivateAccountJSON(filepath.Join(home, "service.json"), config); err != nil {
			return nil, err
		}
		commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(commandCtx, pin.Bundle.Executable, "service", "restart")
		cmd.Dir = p.Env["HOME"]
		cmd.Env = os.Environ()
		for key, value := range p.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.WaitDelay = time.Second
		if err = childenv.Clean(cmd).Run(); err != nil {
			if commandCtx.Err() != nil {
				return nil, commandCtx.Err()
			}
			return nil, errors.New("OpenCode private service could not restart.")
		}
		next.PID = openCodeServicePID(p.Env)
		if next.PID == 0 {
			return nil, errors.New("OpenCode private service did not register its process.")
		}
		if err = writePrivateAccountJSON(marker, next); err != nil {
			return nil, err
		}
	}
	return newResourceLease(ctx, uses, "turn-", nil)
}
