package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestConfigSyncRefusalComesFromEngine(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	var called atomic.Int32
	calls, err := websvc.Bind([]websvc.Binding{{Name: "SyncCatalogAccount", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true,
		Method: func(_ context.Context, kind, harness, account string) error {
			called.Add(1)
			if kind != "mcp" || harness != "pi" || account != "engine-selected" {
				t.Errorf("sync arguments = %q, %q, %q", kind, harness, account)
			}
			return errors.New("verified engine caller refused sync")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	canary := filepath.Join(store.ConfigDir(), "skills", "load-dyna-orchestrator", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(canary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canary, []byte("no local regeneration"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mcp"} {
		cmd := mcpCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"sync", "--harness", "pi", "--account", "engine-selected"})
		err := cmd.ExecuteContext(t.Context()) // Cleared stamps, no CLI actor.
		if err == nil || !strings.Contains(err.Error(), "verified engine caller refused sync") {
			t.Errorf("%s sync = %v", kind, err)
		}
	}
	// The skill catalog has no sync verb: it never writes a native config.
	skill := skillCmd()
	var out bytes.Buffer
	skill.SetOut(&out)
	skill.SetErr(&out)
	skill.SetArgs([]string{"sync", "--harness", "pi", "--account", "engine-selected"})
	if err := skill.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("skill sync = %v, want an unknown command or flag", err)
	}
	if called.Load() != 1 {
		t.Errorf("engine calls = %d, want 1", called.Load())
	}
	if b, err := os.ReadFile(canary); err != nil || string(b) != "no local regeneration" {
		t.Errorf("CLI regenerated locally: %v", err)
	}
}
