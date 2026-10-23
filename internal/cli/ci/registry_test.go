package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/localci"
)

func TestNodeCommandValidatesAndSavesNode(t *testing.T) {
	s := localci.Store{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	run := func(args ...string) (string, error) {
		cmd := nodeCommand(s)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := run("add", "hermes", "--ssh", "pip@hermeslab", "--slots", "2"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("add", "hermes", "--slots", "3"); err != nil {
		t.Fatalf("the alias no longer updates a stored machine: %v", err)
	}
	if _, err := run("add", "hermes", "--slots", "0"); err == nil {
		t.Fatal("accepted zero slots")
	}
	if _, err := run("add", "bad", "--ssh", "user@host;id"); err == nil {
		t.Fatal("accepted SSH shell syntax")
	}
	output, err := run("list", "--json")
	var nodes []localci.Node
	if err != nil || json.Unmarshal([]byte(output), &nodes) != nil || len(nodes) != 1 {
		t.Fatalf("listed nodes = %q, %v", output, err)
	}
	if nodes[0].Slots != 3 || nodes[0].SSH != "pip@hermeslab" || !nodes[0].Enabled {
		t.Fatalf("saved flags = %#v", nodes)
	}
	if _, err := run("disable", "hermes"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("rm", "hermes"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("disable", "hermes"); err == nil {
		t.Fatal("missing node accepted")
	}
}

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
