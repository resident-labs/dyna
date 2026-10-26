package home

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/childenv"
)

// loginPathTimeout bounds the login shell that reports the owner's PATH.
const loginPathTimeout = 5 * time.Second

// ownerLoginPath asks the owner's login shell for its PATH once per process. The
// engine often starts from a service manager whose PATH lacks the owner's tool
// directories (~/.cargo/bin, ~/.local/bin, mise, nix), which a login shell adds
// (D-265). A test binary skips the lookup so no test runs the developer's shell.
var ownerLoginPath = sync.OnceValue(func() string {
	if testing.Testing() {
		return ""
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return loginPathFrom(shell, os.Environ())
})

// loginPathFrom runs `shell -lc` and returns the PATH it writes to a temporary
// file, or "" when the shell fails or does not answer in time. The shell's own
// output goes nowhere, so a background process that shell startup leaves behind
// holds no pipe the caller would wait on.
func loginPathFrom(shell string, env []string) string {
	file, err := os.CreateTemp("", "dyna-login-path-")
	if err != nil {
		return ""
	}
	defer os.Remove(file.Name())
	file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), loginPathTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lc", `printf '%s' "$PATH" >"$0"`, file.Name())
	cmd.Env = env
	if childenv.Clean(cmd).Run() != nil {
		return ""
	}
	out, err := os.ReadFile(file.Name())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// withLoginPath keeps path as it is and appends each absolute directory of the
// login PATH that path lacks, so no existing lookup order changes.
func withLoginPath(path, login string) string {
	have := make(map[string]bool)
	for _, dir := range filepath.SplitList(path) {
		have[dir] = true
	}
	out := path
	for _, dir := range filepath.SplitList(login) {
		if filepath.IsAbs(dir) && !have[dir] {
			have[dir] = true
			if out != "" {
				out += string(os.PathListSeparator)
			}
			out += dir
		}
	}
	return out
}

// loginPathSource is a variable so a test can supply a login PATH.
var loginPathSource = ownerLoginPath
