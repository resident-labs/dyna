package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/fsutil"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/store"
)

func TestPrivateLoginUsesOwnerToolPolicy(t *testing.T) {
	a := testAccounts(t)
	ownerHome := os.Getenv("HOME")
	tools := t.TempDir()
	if err := fsutil.WriteExecutable(filepath.Join(tools, "owner-helper"), []byte("#!/bin/sh\nprintf '%s' \"$HOME\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_BIN", "/fake/dyna")
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DYNA_HOME_TOOLS", "untrusted-missing-tool")
	config := harnessmgr.NewConfigManager()
	if err := os.MkdirAll(filepath.Dir(config.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(config.Path, harnessmgr.HarnessConfig{ToolHomeBridges: []string{"owner-helper"}}, 0600); err != nil {
		t.Fatal(err)
	}
	n, err := a.Create("claude-code", "private login")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/native")
	if err != nil {
		t.Fatal("login did not apply the owner JSON tool policy", err)
	}
	defer flow.Cancel()
	env := map[string]string{}
	for _, pair := range flow.Env {
		key, value, _ := strings.Cut(pair, "=")
		env[key] = value
	}
	if env["DYNA_HOME_TOOLS"] != "owner-helper" || env["HOME"] == ownerHome {
		t.Fatal("login lost private HOME or the owner tool policy")
	}
	cmd := exec.Command(filepath.Join(filepath.Dir(env["DYNA_BIN"]), "owner-helper"))
	cmd.Env = flow.Env
	if raw, err := cmd.Output(); err != nil || string(raw) != ownerHome {
		t.Fatal("login tool bridge did not use owner HOME", err)
	}
}
