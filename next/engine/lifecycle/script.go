package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const (
	// scriptCreateLimit bounds a create, which may boot a machine. The other two only ask for something.
	scriptCreateLimit = 15 * time.Minute
	scriptLimit       = 5 * time.Minute
	// scriptErrorBytes is how much of stderr goes with a failure, from its end.
	scriptErrorBytes = 1 << 10
)

// Script runs a pool's script on the engine host, as the engine's user, in the pool's folder. It gets a bare environment, so
// nothing the engine holds reaches it.
type Script struct{ Layout box.Layout }

func (s Script) run(ctx context.Context, pool string, limit time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	path := s.Layout.PoolScript(pool)
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = filepath.Dir(path)
	for _, name := range []string{"PATH", "HOME", "USER", "LANG", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second // a child that keeps the pipe open must not hold the step
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	text := strings.TrimSpace(stderr.String())
	if text != "" {
		log.Printf("pool %s: %s: %s", pool, args[0], text)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	if len(text) > scriptErrorBytes {
		text = strings.ToValidUTF8(text[len(text)-scriptErrorBytes:], "")
	}
	if text != "" {
		return nil, fmt.Errorf("%s: %w: %s", args[0], err, text)
	}
	return nil, fmt.Errorf("%s: %w", args[0], err)
}

// Create asks for a machine and returns how to reach it.
func (s Script) Create(ctx context.Context, pool string, name contract.HostName, size contract.BoxSizeName) (string, error) {
	out, err := s.run(ctx, pool, scriptCreateLimit, "create", string(name), string(size))
	if err != nil {
		return "", err
	}
	var reply struct {
		SSH string `json:"ssh"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return "", fmt.Errorf("create: stdout is not the JSON that was expected: %w", err)
	}
	return sshDestination(reply.SSH)
}

// sshDestination turns user@host:port into the form ssh takes.
func sshDestination(text string) (string, error) {
	user, address, _ := strings.Cut(text, "@")
	if user == "" || address == "" || strings.Count(text, "@") != 1 || strings.HasPrefix(text, "-") || strings.ContainsAny(text, " \t\r\n/") {
		return "", fmt.Errorf("create: %q is not user@host:port", text)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return text, nil
	}
	return "ssh://" + text, nil
}

// Destroy gives a machine back.
func (s Script) Destroy(ctx context.Context, pool string, name contract.HostName) error {
	_, err := s.run(ctx, pool, scriptLimit, "destroy", string(name))
	return err
}

// List names the machines the script still has.
func (s Script) List(ctx context.Context, pool string) ([]string, error) {
	out, err := s.run(ctx, pool, scriptLimit, "list")
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		return nil, errors.Join(errors.New("list: stdout is not a JSON list"), err)
	}
	return names, nil
}
