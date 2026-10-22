package gitexec

import (
	"context"
	"strings"
	"testing"
)

func TestCommandDropsOverridesAndTokens(t *testing.T) {
	t.Setenv("GIT_DIR", "/wrong")
	t.Setenv("GIT_SSH_COMMAND", "interactive")
	t.Setenv("GH_TOKEN", "wrong-token")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "wrong-enterprise-token")
	cmd := Command(context.Background(), t.TempDir(), "status")
	env := strings.Join(cmd.Env, "\n")
	for _, bad := range []string{"GIT_DIR=/wrong", "interactive", "wrong-token", "wrong-enterprise-token"} {
		if strings.Contains(env, bad) {
			t.Fatalf("inherited %s", bad)
		}
	}
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -oBatchMode=yes"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "core.hooksPath=/dev/null") {
		t.Fatal("hooks enabled")
	}
}
