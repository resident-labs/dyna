package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestTransferCLIStatusHandoffCancelAndHistory(t *testing.T) {
	storetest.Isolate(t)
	p := profile.Profile{Name: "next", Harness: profile.HarnessMock, Model: "default-model", Taste: 5, Intelligence: 5, Cost: 5}
	m := subagent.NewManager()
	work := workRoom(t, m)
	m.Profiles = func() (*profile.Store, error) { return &profile.Store{Profiles: []profile.Profile{p}}, nil }
	m.TransferLaunch = func(*subagent.Manager, string, string) error { return nil }
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "worker", RoomID: work.ID, Room: work.Name, Dir: t.TempDir(), Depth: 1, Profile: "old", Model: "prior-model", Turns: 2, Resumable: true, HarnessSession: "native-old", Created: time.Now()}
	dir, _ := m.Store.Dir(meta.ID)
	os.MkdirAll(filepath.Join(dir, "turns"), 0700)
	write := func(path string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	recordtest.Write(t, m.Store.Root, meta.ID, meta)
	write(filepath.Join(dir, "turns", "1.json"), subagent.Turn{N: 1, Profile: "old", Model: "prior-model", Status: subagent.TurnOK, Prompt: "Build a decoder", Result: "MarkerOne: parser changed", HarnessSession: "native-old"})
	write(filepath.Join(dir, "turns", "2.json"), subagent.Turn{N: 2, Profile: "old", Model: "prior-model", Status: subagent.TurnOK, Prompt: "Next: run tests", Result: "MarkerTwo: tests passed", HarnessSession: "native-old"})
	serveSessionControls(t, m)
	call := func(args ...string) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := call("transfer", meta.ID, "--profile", "next", "--model", "chosen-model", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var state subagent.TransferState
	if err := json.Unmarshal([]byte(out), &state); err != nil || state.Model != "chosen-model" {
		t.Fatalf("request: %s %v", out, err)
	}
	if out, err := call("transfer", "status", meta.ID, "--json"); err != nil || !strings.Contains(out, state.ID) {
		t.Fatalf("status: %s %v", out, err)
	}
	file := filepath.Join(t.TempDir(), "handoff.txt")
	os.WriteFile(file, []byte("Finish the decoder. Actual work is on branch decoder."), 0600)
	if out, err := call("transfer", "handoff", meta.ID, "@"+file, "--token", state.ID, "--dir", meta.Dir, "--branch", "decoder"); err != nil || !strings.Contains(out, "Handoff saved") {
		t.Fatalf("handoff: %s %v", out, err)
	}
	if out, err := call("history", meta.ID, "--from-turn", "1", "--to-turn", "1", "--search", "Marker"); err != nil || !strings.Contains(out, "MarkerOne") || strings.Contains(out, "MarkerTwo") {
		t.Fatalf("history range: %s %v", out, err)
	}
	if out, err := call("history", meta.ID, "--search", "Marker", "--tail", "1"); err != nil || !strings.Contains(out, "MarkerTwo") || strings.Contains(out, "MarkerOne") {
		t.Fatalf("history tail: %s %v", out, err)
	}
	if out, err := call("transfer", "cancel", meta.ID); err != nil || !strings.Contains(out, "cancelled") {
		t.Fatalf("cancel: %s %v", out, err)
	}
	if _, err := call("transfer", "handoff", meta.ID, "late", "--token", state.ID); err == nil {
		t.Fatal("accepted stale handoff")
	}
}
