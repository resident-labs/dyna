package computer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/store"
)

// A holder is a background Node process attached to the shared browser over CDP.
// CDP overrides and listeners live only as long as their connection, so each
// trace and each preset runs in a holder. A holder exits on SIGTERM or when the browser goes away.

// holderScripts are written next to every holder. browser_page.mjs picks the page they act on.
var holderScripts = map[string][]byte{"browser_page.mjs": pageScript}

// holderSpawn starts a holder and returns its pid. Tests replace it.
var holderSpawn = func(dir string, args ...string) (int, error) {
	cmd := exec.Command("node", args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go cmd.Wait()
	return cmd.Process.Pid, nil
}

func holderDir(id, name string) string {
	return filepath.Join(store.DataDir(), "browser", name, id)
}

// holderPID returns the pid of the running holder in dir, or 0.
// The pid file holds "pid start-time". A file whose process is gone, or whose pid another
// process now uses (a crash or reboot leaves such files), is removed and counts as no holder.
func holderPID(dir string) int {
	path := filepath.Join(dir, "pid")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pid int
	var start string
	if n, _ := fmt.Sscan(string(b), &pid, &start); n == 2 {
		if now, ok := computers.ProcessStart(pid); ok && now == start {
			return pid
		}
	}
	os.Remove(path)
	return 0
}

func waitForFile(ctx context.Context, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("holder did not write %s: %w", filepath.Base(path), ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// startHolder runs script in a new holder named name and waits until it writes "ready".
// A running holder of the same name makes it fail unless replace is set, which stops that one first.
func startHolder(ctx context.Context, id, name string, script []byte, replace bool, args ...string) (string, error) {
	var browser computers.Browser
	if err := computers.NewClient(id).Call(ctx, computers.Request{Op: "browser.start"}, &browser); err != nil {
		return "", err
	}
	module, err := playwrightModule(ctx)
	if err != nil {
		return "", err
	}
	dir := holderDir(id, name)
	if pid := holderPID(dir); pid > 0 {
		if !replace {
			return "", fmt.Errorf("a %s is already running; stop it first", name)
		}
		if err := killHolder(ctx, pid); err != nil {
			return "", err
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files := map[string][]byte{name + ".mjs": script}
	for n, b := range holderScripts {
		files[n] = b
	}
	for n, b := range files {
		if err := fsutil.WriteFile(filepath.Join(dir, n), b, 0o644, false); err != nil {
			return "", err
		}
	}
	pid, err := holderSpawn(dir, append([]string{filepath.Join(dir, name+".mjs"), module, browser.Endpoint, dir}, args...)...)
	if err != nil {
		return "", err
	}
	start, _ := computers.ProcessStart(pid)
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d %s", pid, start)), 0o600); err != nil {
		return "", err
	}
	if err := waitForFile(ctx, filepath.Join(dir, "ready"), 30*time.Second); err != nil {
		syscall.Kill(pid, syscall.SIGKILL)
		return "", err
	}
	return dir, nil
}

// killHolder sends SIGTERM and waits for the process to exit.
func killHolder(ctx context.Context, pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for syscall.Kill(pid, 0) == nil {
		select {
		case <-ctx.Done():
			return errors.New("holder did not exit after SIGTERM")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return nil
}
