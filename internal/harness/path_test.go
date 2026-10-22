package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/profile"
)

func pathTestCLI(t *testing.T, dir, label string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"text\",\"part\":{\"text\":\"" + label + "\"}}'\n"
	if err := fsutil.WriteExecutable(filepath.Join(dir, "opencode"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
func TestCustomCommandKeepsExistingPATHLookup(t *testing.T) {
	host := t.TempDir()
	override := t.TempDir()
	home := t.TempDir()
	pathTestCLI(t, host, "host")
	pathTestCLI(t, override, "override")
	t.Setenv("PATH", host)
	p := profile.Profile{Name: "custom", Harness: profile.HarnessCustom, Command: []string{"opencode"}, Env: map[string]string{"PATH": override, "HOME": home}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := RunWithRecovery(ctx, p, "task", t.TempDir(), false)
	if err != nil || !strings.Contains(result.Output, "host") || strings.Contains(result.Output, "override") {
		t.Fatalf("custom lookup changed: result=%#v err=%v", result, err)
	}
}
