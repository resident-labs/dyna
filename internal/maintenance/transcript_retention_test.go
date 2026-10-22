package maintenance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/workeroutput"
)

func TestRetentionPreservesTranscriptSource(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)
	id := "ses_20261001-000000-abcdef12"
	dir := filepath.Join(runstore.SessionsDir(), id)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	meta := subagent.Meta{ID: id, Name: "fixture", RoomID: "room_20261001-000000-abcdef12", Room: "work", Created: old, Closed: old, Turns: 1}
	recordtest.Write(t, runstore.SessionsDir(), id, meta)
	var normalized bytes.Buffer
	for _, event := range []workeroutput.Event{
		{V: 1, Kind: "reasoning", State: "completed", Text: "retained reasoning"},
		{V: 1, Kind: "message", Role: "assistant", Text: "intermediate reply"},
		{V: 1, Kind: "tool", ID: "tool-1", Name: "command", State: "started", Input: "echo retained"},
		{V: 1, Kind: "tool", ID: "tool-1", Name: "command", State: "completed", Output: "retained"},
		{V: 1, Kind: "message", Role: "assistant", Text: "final answer"},
	} {
		if err := json.NewEncoder(&normalized).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(dir, "agents", "1", "output.jsonl")
	if err := os.WriteFile(output, normalized.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(dir, "agents", "1", "stdout.log")
	const rawOutput = "raw harness output"
	if err := os.WriteFile(raw, []byte(rawOutput), 0600); err != nil {
		t.Fatal(err)
	}
	turn := subagent.Turn{N: 1, Status: subagent.TurnOK, Prompt: "task", Result: "final answer", Created: old, Started: old, Ended: old, NormEnd: int64(normalized.Len())}
	if err := store.WriteJSON(filepath.Join(dir, "turns", "1.json"), turn, 0600); err != nil {
		t.Fatal(err)
	}
	sessions := &subagent.Store{Root: runstore.SessionsDir()}
	before := transcript.NewLoader(sessions)
	all, err := before.Load(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	focused, err := before.LoadTurn(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Turns) != 1 || len(all.Turns[0].Items) < 4 {
		t.Fatalf("fixture lacks real transcript events: %+v", all.Turns)
	}
	// The first pass at this retention age only measures.
	if _, err := maintenance.Run(context.Background(), maintenance.Options{Now: now, Repos: []string{}}); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Run(context.Background(), maintenance.Options{Now: now, Repos: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	// A fresh loader cannot hide a deleted source behind its finished-turn cache.
	after := transcript.NewLoader(sessions)
	gotAll, err := after.Load(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	gotFocused, err := after.LoadTurn(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all, gotAll) {
		t.Error("Load lost transcript content after retention")
	}
	if !reflect.DeepEqual(focused, gotFocused) {
		t.Error("LoadTurn lost transcript content after retention")
	}
	if data, err := os.ReadFile(output); err != nil || !bytes.Equal(data, normalized.Bytes()) {
		t.Errorf("normalized transcript changed: %v", err)
	}
	if _, err := os.Stat(raw); !os.IsNotExist(err) {
		t.Errorf("expired raw log remains: %v", err)
	}
	if result.BytesFreed != int64(len(rawOutput)) {
		t.Errorf("freed %d bytes, want raw log only", result.BytesFreed)
	}
}
