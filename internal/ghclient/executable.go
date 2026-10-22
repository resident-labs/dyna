package ghclient

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/childenv"
)

var executables sync.Map

// executable resolves a mise wrapper once without running its global setup.
func executable(ctx context.Context) string {
	if inherited := os.Getenv(BinaryEnv); filepath.IsAbs(inherited) && isExecutable(inherited) {
		return inherited
	}

	path, err := exec.LookPath("gh")
	if err != nil {
		return "gh"
	}
	resolve, _ := executables.LoadOrStore(path, sync.OnceValue(func() string {
		file, err := os.Open(path)
		if err != nil {
			return path
		}
		header := make([]byte, 1024)
		n, _ := io.ReadFull(file, header)
		file.Close()
		text := string(header[:n])
		if !strings.HasPrefix(text, "#!") || !strings.Contains(text, "mise x") {
			return path
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := childenv.Clean(exec.CommandContext(ctx, "mise", "which", "gh"))
		if out, err := cmd.Output(); err == nil {
			resolved := strings.TrimSpace(string(out))
			if resolved != path && isExecutable(resolved) {
				return resolved
			}
		}
		if isExecutable("/usr/bin/gh") {
			return "/usr/bin/gh"
		}
		return path
	}))
	return resolve.(func() string)()
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}
