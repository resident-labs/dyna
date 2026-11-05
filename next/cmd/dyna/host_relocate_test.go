package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRelocateEngineCLIRefusesLiveEngine(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := newRootCmd()
	root.SetArgs([]string{"host", "relocate-engine", "--data", dir, "--from", "old", "--from-ssh", "pip@100.122.131.34", "--to", "new"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "in use by another engine") {
		t.Fatalf("want exclusive engine lock refusal, got %v", err)
	}
}

func TestRelocateEngineCLIWritesExistingStoreWithoutSocket(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []box.Host{{Name: "old", SSH: "pip@localhost"}, {Name: "new", SSH: "pip@100.106.214.124"}} {
		if err := s.AddHost(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd()
	root.SetArgs([]string{"--socket", filepath.Join(t.TempDir(), "absent.sock"), "host", "relocate-engine", "--data", dir, "--from", "old", "--from-ssh", "pip@100.122.131.34", "--to", "new"})
	out := &bytes.Buffer{}
	root.SetOut(out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "old") || !strings.Contains(out.String(), "new") {
		t.Fatalf("missing summary: %s", out)
	}
	s, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hosts, err := s.Hosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if host.EngineHost == nil || *host.EngineHost != (host.Name == "new") {
			t.Fatalf("identity: %+v", host)
		}
		if host.Name == "old" && host.SSH != "pip@100.122.131.34" {
			t.Fatalf("address: %+v", host)
		}
	}
}
