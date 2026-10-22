package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeFixtureCommand(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700); err != nil {
		t.Fatal("Cannot create fixture command")
	}
	return path
}
func TestClaudeModelsNoInventedFallback(t *testing.T) {
	command := claudeFixtureCommand(t, "test \"$*\" = '--help'\nprintf '%s\\n' \"  --model <model> aliases 'sonnet', 'opus', 'sonnet', 'claude-example-1'\" \"  --other 'secret-option'\"\n")
	models, err := modelsClaude(context.Background(), command, nil)
	if err != nil || len(models) != 2 || models[0].ID != "sonnet" || models[1].ID != "opus" {
		t.Fatal("Unexpected model aliases")
	}
	empty := claudeFixtureCommand(t, "printf '%s' 'no model contract'\n")
	if models, err := modelsClaude(context.Background(), empty, nil); err == nil || len(models) != 0 {
		t.Fatal("Invented fallback models")
	}
}

// The help shape is copied from Claude Code 2.1.302.
func TestClaudeModelsCarryHelpEfforts(t *testing.T) {
	command := claudeFixtureCommand(t, "printf '%s\\n' \"  --effort <level>     Effort level for the current session\" \"                       (low, medium, high, xhigh, max)\" \"  --model <model> aliases 'sonnet', 'opus'\"\n")
	models, err := modelsClaude(context.Background(), command, nil)
	if err != nil || len(models) != 2 || len(models[1].Efforts) != 5 {
		t.Fatalf("models = %+v, %v", models, err)
	}
	if e := models[0].Efforts[3]; e.ID != "xhigh" || strings.Join(e.Args, " ") != "--effort xhigh" {
		t.Fatalf("effort = %+v", e)
	}
	plain := claudeFixtureCommand(t, "printf '%s\\n' \"  --model <model> aliases 'sonnet'\"\n")
	if models, err := modelsClaude(context.Background(), plain, nil); err != nil || models[0].Efforts != nil {
		t.Fatalf("invented efforts: %+v %v", models, err)
	}
}
