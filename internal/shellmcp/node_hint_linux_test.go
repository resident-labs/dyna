//go:build linux

package shellmcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestShellHintsJobRunForSSHToRegisteredNode fakes ssh on PATH, so the
// command never reaches a host. The command still runs and its answer names
// the node and the dyna job run form for builds and tests.
func TestShellHintsJobRunForSSHToRegisteredNode(t *testing.T) {
	tools := newTools(t, time.Minute)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho reached $1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tools.Nodes = func(context.Context) ([]Node, error) {
		return []Node{{Name: "pve", SSH: "fl-rea-pve"}}, nil
	}
	text := call(t, tools, "shell", map[string]any{"command": "ssh fl-rea-pve uptime", "description": "Read uptime on the node"})
	if !strings.Contains(text, "reached fl-rea-pve") || !strings.Contains(text, "dyna job run --node pve --commit HEAD --workspace --io heavy -- CMD") {
		t.Fatalf("result = %q", text)
	}
}
