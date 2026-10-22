package configsvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceProfileProbePolicy(t *testing.T) {
	isolate(t)
	seed(t, mock("test"))
	s := &Service{ReadOnly: true}
	if _, err := s.ConfigCheckProfile(context.Background(), "test", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigCheckProfile(context.Background(), "missing", true); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("live probe policy: %v", err)
	}
	s.ReadOnly = false
	if _, err := s.ConfigCheckProfile(context.Background(), "test", true); err != nil {
		t.Fatal(err)
	}
}

func TestServiceUpdateNotice(t *testing.T) {
	isolate(t)
	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "dyna")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"lastChecked":%q,"release":{"tag_name":"v9.9.9"}}`, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(filepath.Join(dir, "update-check.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(cli, []byte(`#!/bin/sh
[ "$1" = version ] || exit 1
[ -z "${DYNA_SUBAGENT_LOCK_FD:-}" ] || exit 2
printf '  dyna v0.4.1  \n'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DYNA_SUBAGENT_LOCK_FD", "must-not-leak")
	s := &Service{CLI: cli}
	if got := s.UpdateNotice(context.Background()); got.Current != "v0.4.1" || got.Latest != "v9.9.9" {
		t.Fatalf("notice: %+v", got)
	}
	s.NoUpdate = true
	if got := s.UpdateNotice(context.Background()); got != (UpdateNotice{}) {
		t.Fatalf("disabled: %+v", got)
	}
	s.NoUpdate = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := s.UpdateNotice(ctx); got != (UpdateNotice{}) {
		t.Fatalf("canceled: %+v", got)
	}
	s.CLI = ""
	if got := s.UpdateNotice(context.Background()); got != (UpdateNotice{}) {
		t.Fatalf("no CLI: %+v", got)
	}
}
