package harnessconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestCatalogListUsesTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"test":{"command":"node"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list", "--harness", "claude"}} {
		cmd := NewMCPCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte(`"test"`)) {
			t.Fatal("catalog list read personal MCP entry")
		}
		if out.Len() == 0 {
			t.Fatal("empty result")
		}
	}
}

func TestPersonalImportCommandIsRetired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cmd := NewMCPCommand()
	cmd.SetArgs([]string{"import"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("personal import still exists")
	}
}

func TestGeneratedMCPServeCommandRemainsHidden(t *testing.T) {
	cmd, _, err := NewMCPCommand().Find([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "serve" || !cmd.Hidden || cmd.RunE == nil {
		t.Fatal("generated MCP serve command is unavailable")
	}
	if err := cmd.ValidateArgs([]string{"generation", "server"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.ValidateArgs([]string{"generation"}); err == nil {
		t.Fatal("serve accepted missing server")
	}
}

func TestSessionResolverFindsTheSessionByHarnessID(t *testing.T) {
	root, work := t.TempDir(), t.TempDir()
	id := "ses_20261020-000000-abcdef12"
	if err := os.Mkdir(filepath.Join(root, id), 0700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"id": id, "name": "a", "roomId": "room-1", "room": "work", "dir": work, "harnessSession": "oc-ses-1", "depth": 2}
	recordtest.Write(t, root, id, meta)
	resolve := sessionResolver(&subagent.Store{Root: root})
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("DYNA_SCRATCH", "/stale/other-session")
	got, ok := resolve("oc-ses-1")
	if !ok || got.Owner != id || got.RoomID != "room-1" || got.Cwd != work || !strings.Contains(strings.Join(got.Env, " "), "DYNA_SESSION="+id) {
		t.Errorf("resolved = %+v, %v", got, ok)
	}
	scratchDir := ""
	for _, kv := range got.Env {
		if v, found := strings.CutPrefix(kv, "DYNA_SCRATCH="); found {
			scratchDir = v
		}
	}
	if scratchDir == "" || strings.Contains(scratchDir, "stale") || !strings.Contains(scratchDir, id) {
		t.Errorf("scratch = %q, want this session's own directory", scratchDir)
	}
	if _, ok := resolve("unknown"); ok {
		t.Error("an unknown harness session resolved")
	}
}

func TestSessionCwdPrefersTheSessionDirectory(t *testing.T) {
	root, work := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "ses_cwd"), 0700); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"ses_cwd","roomId":"room-1","room":"dyna-agent","dir":` + strconv.Quote(work) + `}`
	recordtest.Write(t, root, "ses_cwd", json.RawMessage(meta))
	store := &subagent.Store{Root: root}
	if got := sessionCwd(store, "ses_cwd", "/plugin"); got != work {
		t.Errorf("session cwd = %q, want %q", got, work)
	}
	if got := sessionCwd(store, "ses_unknown", "/plugin"); got != "/plugin" {
		t.Errorf("unknown session cwd = %q, want the fallback", got)
	}
}
