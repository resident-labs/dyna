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
	// Match whole entries, so an unrelated variable that happens to hold one of
	// these words as part of its value does not fail the test.
	env := make(map[string]string, len(cmd.Env))
	for _, entry := range cmd.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	for _, bad := range []string{"GIT_DIR", "GH_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		if _, ok := env[bad]; ok {
			t.Fatalf("inherited %s", bad)
		}
	}
	if want := map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_SSH_COMMAND": "ssh -oBatchMode=yes"}; env["GIT_TERMINAL_PROMPT"] != want["GIT_TERMINAL_PROMPT"] || env["GIT_SSH_COMMAND"] != want["GIT_SSH_COMMAND"] {
		t.Fatalf("prompt = %q, ssh command = %q", env["GIT_TERMINAL_PROMPT"], env["GIT_SSH_COMMAND"])
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "core.hooksPath=/dev/null") {
		t.Fatal("hooks enabled")
	}
}
