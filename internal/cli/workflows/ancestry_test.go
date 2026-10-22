package workflows

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
)

func TestRunCommandReturnsAncestryWriteFailure(t *testing.T) {
	inRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_RUN_ID", "wf_source")
	source, err := runstore.Create("source", "return 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	source.Finish("ok", "1", nil)
	config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dyna")
	profiles := &profile.Store{Path: filepath.Join(config, "profiles.json"), Profiles: []profile.Profile{{
		Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
	}}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(context.Context, string, string) (string, error) {
			return "", errors.New("engine refused ancestry")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	t.Setenv("DYNA_RUN_ID", "wf_failed_cli")
	dir := t.TempDir()
	script := filepath.Join(dir, "workflow.js")
	if err := os.WriteFile(script, []byte("return 7"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := NewRunCommand()
	cmd.SetOut(io.Discard)
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{script, "--resume", source.Meta.ID, "--quiet", "--dir", dir})
	if err := cmd.Execute(); err == nil || !strings.Contains(stderr.String(), "engine refused ancestry") {
		t.Fatalf("run command = %v, stderr = %s", err, stderr.String())
	}
}
