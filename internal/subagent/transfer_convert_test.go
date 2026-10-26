package subagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// claudeTestProfile is a Claude Code target. Tests that stop before the turn
// never launch it; the conversion is what they check.
func claudeTestProfile(name, model string) profile.Profile {
	return profile.Profile{Name: name, Harness: profile.HarnessClaudeCode, Model: model, Taste: 5, Intelligence: 5, Cost: 5}
}

// testSessionVersions pins both harnesses to the versions the converter knows.
func testSessionVersions(harness string) (string, error) {
	switch harness {
	case profile.HarnessPi:
		return "1.4.0", nil
	case profile.HarnessClaudeCode:
		return "2.1.309", nil
	}
	return "", os.ErrNotExist
}

// writePiSession writes a Pi session file with one tool call for id in the
// shared Pi session store, where the converter reads it.
func writePiSession(t *testing.T, id, cwd string) {
	t.Helper()
	home, err := harnessruntime.NewAccounts().HarnessHome(profile.HarnessPi)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-10-25T10:00:00Z","cwd":"` + cwd + `"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"keep the decoder work"}]}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"Reading the parser."},{"type":"toolCall","id":"call_pi_1","name":"read","arguments":{"path":"parser.go"}}]}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-10-25T10:00:03Z","message":{"role":"toolResult","toolCallId":"call_pi_1","toolName":"read","content":[{"type":"text","text":"package parser"}],"isError":false}}`,
		`{"type":"message","id":"a2","parentId":"t1","timestamp":"2026-10-25T10:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"The parser looks correct."}]}}`,
	}
	path := filepath.Join(dir, "2026-10-25T10-00-00-000Z_"+id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// claudeEntriesAt decodes a written Claude session file.
type claudeEntryAt struct {
	ParentUUID *string `json:"parentUuid"`
	Type       string  `json:"type"`
	SessionID  string  `json:"sessionId"`
	Cwd        string  `json:"cwd"`
	UUID       string  `json:"uuid"`
	Message    struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			ID        string `json:"id"`
			ToolUseID string `json:"tool_use_id"`
		} `json:"content"`
	} `json:"message"`
}

func readClaudeSession(t *testing.T, path string) []claudeEntryAt {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []claudeEntryAt
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry claudeEntryAt
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("claude entry: %v: %s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

// TestPiTransferConvertsTheSessionForClaudeCode checks that a Pi to Claude
// transfer writes a chained Claude session, resumes by its new id, clears the
// Pi runtime pin and keeps the source file untouched.
func TestPiTransferConvertsTheSessionForClaudeCode(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := claudeTestProfile("claude-next", "anthropic/claude-opus-5-5")
	e := newTestEnv(t, old, next)
	e.m.InstalledVersion = testSessionVersions
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	writePiSession(t, first.HarnessSession, info.Dir)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "claude-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if state.Source != sourceConverted || state.ConvertedSession == "" {
		t.Fatalf("transfer did not plan a conversion: %+v", state)
	}
	advance(t, e, state, 1)
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "starting" || got.Source != sourceConverted || !strings.Contains(got.Reason, "converted the Pi session") {
		t.Fatalf("conversion not recorded: %+v", got)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Profile != "claude-next" || meta.Model != "anthropic/claude-opus-5-5" || !meta.Resumable {
		t.Fatalf("target was not applied: %+v", meta)
	}
	if meta.HarnessSession != got.ConvertedSession || meta.HarnessSession == first.HarnessSession {
		t.Fatalf("target session id = %q, want converted %q", meta.HarnessSession, got.ConvertedSession)
	}
	if meta.RuntimePin != nil {
		t.Fatalf("cross-harness transfer kept the Pi runtime pin: %+v", meta.RuntimePin)
	}
	home, err := harnessruntime.NewAccounts().HarnessHome(profile.HarnessClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.NewReplacer("/", "-", ".", "-").Replace(info.Dir)
	claudePath := filepath.Join(home, "projects", key, got.ConvertedSession+".jsonl")
	entries := readClaudeSession(t, claudePath)
	if len(entries) != 4 || entries[0].ParentUUID != nil || entries[0].SessionID != got.ConvertedSession || entries[0].Cwd != info.Dir {
		t.Fatalf("claude session = %+v", entries)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentUUID == nil || *entries[i].ParentUUID != entries[i-1].UUID {
			t.Fatalf("claude entry %d is not chained: %+v", i, entries[i])
		}
	}
	call := entries[1].Message.Content[1]
	if call.Type != "tool_use" || call.ID == "call_pi_1" {
		t.Fatalf("tool call = %+v", call)
	}
	result := entries[2].Message.Content[0]
	if result.Type != "tool_result" || result.ToolUseID != call.ID {
		t.Fatalf("tool result = %+v", result)
	}
	if !strings.Contains(got.Handoff, "converted from Pi") {
		t.Fatalf("replacement prompt does not explain the conversion: %s", got.Handoff)
	}
}

// TestPiTransferFallsBackWhenTheConverterCannotWrite checks the version pin:
// a Claude Code version the writer does not know falls back to the written
// handoff and says why.
func TestPiTransferFallsBackWhenTheConverterCannotWrite(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := claudeTestProfile("claude-next", "anthropic/claude-opus-5-5")
	e := newTestEnv(t, old, next)
	e.m.InstalledVersion = func(harness string) (string, error) {
		if harness == profile.HarnessClaudeCode {
			return "2.1.310", nil
		}
		return "1.4.0", nil
	}
	e.m.TransferNow = func() time.Time { return time.Now() }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	writePiSession(t, first.HarnessSession, info.Dir)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "claude-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	advance(t, e, state, 1)
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "starting" || got.Source != "transcript" || !strings.Contains(got.Reason, "not a supported target") {
		t.Fatalf("version pin did not fall back: %+v", got)
	}
	meta, err := e.m.Store.ReadMeta(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.HarnessSession != "" || meta.Profile != "claude-next" || meta.RuntimePin != nil {
		t.Fatalf("fallback state = %+v", meta)
	}
}

// A converted Claude session that the target cannot load falls back to the
// written handoff on its first turn, and the transfer says why.
func TestPiTransferFallsBackWhenTheConvertedSessionDoesNotLoad(t *testing.T) {
	path, logPath := installFakePiTurn(t)
	old := piTestProfile("pi-old", "old/provider-model", path, logPath)
	next := claudeTestProfile("claude-next", "anthropic/claude-opus-5-5")
	e := newTestEnv(t, old, next)
	e.m.InstalledVersion = testSessionVersions
	now := time.Now()
	e.m.TransferNow = func() time.Time { return now }
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	info, _ := e.start("RESPOND: keep the decoder work", func(so *StartOptions) { so.Profile = "pi-old" })
	info, first := e.wait(info.ID, 1)
	writePiSession(t, first.HarnessSession, info.Dir)
	state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "claude-next", WaitSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	advance(t, e, state, 1)
	runner := e.m.Spawner
	held := &heldTurnSpawner{}
	e.m.Spawner = held
	advance(t, e, state, 1)
	if held.lock == nil {
		t.Fatal("replacement turn did not start")
	}
	if _, err := e.m.Store.UpdateTurn(info.ID, 2, func(turn *Turn) {
		turn.Status, turn.Ended, turn.Error = TurnError, now, "session not found"
	}); err != nil {
		t.Fatal(err)
	}
	held.lock.Close()
	e.m.Spawner = runner
	advance(t, e, state, 1)
	got, err := e.m.TransferStatus(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "stopping" || got.Source != "transcript" || !strings.Contains(got.Reason, "converted Claude session did not load") {
		t.Fatalf("load failure did not fall back: %+v", got)
	}
}
