// Package gitexec runs Git without hooks, prompts, or inherited Git overrides.
package gitexec

import (
	"context"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/processguard"
	"os"
	"os/exec"
	"strings"
)

func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := processguard.Command(ctx, "/usr/bin/git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "GIT_") && key != "GH_TOKEN" && key != "GH_ENTERPRISE_TOKEN" && key != "GITHUB_TOKEN" && key != "GITHUB_ENTERPRISE_TOKEN" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_MERGE_AUTOEDIT=no", "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	return childenv.Clean(cmd)
}
