package ci

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/localci"
)

func TestModeCommandUsesResolvedRoomAndRejectsInvalidInput(t *testing.T) {
	s := localci.Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	var resolveCalls int
	resolve := func() (string, error) { resolveCalls++; return "stamped-room", nil }
	run := func(args ...string) (string, error) {
		cmd := modeCommand(s, resolve)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("invalid"); err == nil || resolveCalls != 0 {
		t.Fatalf("invalid mode reached room resolution: %v, %d", err, resolveCalls)
	}
	if out, err := run("both"); err != nil || strings.TrimSpace(out) != "both" {
		t.Fatalf("set mode = %q, %v", out, err)
	}
	if mode, err := s.Mode(context.Background(), "stamped-room"); err != nil || mode != localci.ModeBoth {
		t.Fatalf("resolved room mode = %q, %v", mode, err)
	}
	blocked := errors.New("room stamp is missing")
	cmd := modeCommand(s, func() (string, error) { return "", blocked })
	cmd.SetArgs([]string{"local"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); !errors.Is(err, blocked) {
		t.Fatalf("room resolution failure = %v", err)
	}
}
