package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/workeroutput"
)

// These golden bytes pin the previous writeJSONAtomic format through real
// subagent callers, including escaping and the final newline. Turn prompt
// encoding sorts object keys; other records retain their struct field order.
func TestStorageGoldenBytes(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	s := &Store{Root: t.TempDir()}
	m := Meta{
		ID: "ses_20260102-030405-abcdef01", Name: "golden",
		RoomID: "global", Room: "global",
		Profile: "mock", Dir: "/work", Depth: 1,
		Created: at, LastActivity: at, Status: StatusIdle, Turns: 2, Resumable: true,
	}
	dir, err := s.Dir(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.writeMeta(m); err != nil {
		t.Fatal(err)
	}
	got, err := recordtest.ReadRaw(t, s.Root, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"id\": \"ses_20260102-030405-abcdef01\",\n  \"name\": \"golden\",\n  \"roomId\": \"global\",\n  \"room\": \"global\",\n  \"profile\": \"mock\",\n  \"dir\": \"/work\",\n  \"depth\": 1,\n  \"created\": \"2026-01-02T03:04:05Z\",\n  \"lastActivity\": \"2026-01-02T03:04:05Z\",\n  \"status\": \"idle\",\n  \"closed\": \"0001-01-01T00:00:00Z\",\n  \"turns\": 2,\n  \"resumable\": true,\n  \"usage\": {}\n}\n"
	// The record is stored compact; the fields and their order stay the old ones.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(want)); err != nil {
		t.Fatal(err)
	}
	if string(got) != compact.String() {
		t.Fatalf("session bytes = %q; want %q", got, compact.String())
	}
	// Compare the old encoder directly as well as the literal golden.
	old, err := json.Marshal(m)
	if err != nil || string(got) != string(old) {
		t.Fatal("old session encoder differs")
	}
	tr := Turn{
		N: 1, Prompt: "RESPOND: ready", Status: TurnOK, Profile: "mock",
		Created: at, Started: at, Ended: at, NormStart: 3, NormEnd: 9,
		ReportTo: []string{"@parent"},
	}
	if err := s.writeTurn(m.ID, tr); err != nil {
		t.Fatal(err)
	}
	turnPath := filepath.Join(dir, "turns", "1.json")
	got, err = os.ReadFile(turnPath)
	if err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"deadline\": \"0001-01-01T00:00:00Z\",\n  \"profile\": \"mock\",\n  \"n\": 1,\n  \"prompt\": \"RESPOND: ready\",\n  \"status\": \"ok\",\n  \"created\": \"2026-01-02T03:04:05Z\",\n  \"started\": \"2026-01-02T03:04:05Z\",\n  \"ended\": \"2026-01-02T03:04:05Z\",\n  \"usage\": {},\n  \"usageNormalization\": {\n    \"version\": 0,\n    \"source\": \"\",\n    \"input\": \"\",\n    \"cachedInput\": \"\",\n    \"output\": \"\",\n    \"cost\": \"\"\n  },\n  \"rawStart\": 0,\n  \"normStart\": 3,\n  \"normEnd\": 9,\n  \"reportTo\": [\n    \"@parent\"\n  ]\n}\n"
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(want), &fields); err != nil {
		t.Fatal(err)
	}
	sorted, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = string(append(sorted, '\n'))
	if string(got) != want {
		t.Fatalf("turn bytes = %q; want %q", got, want)
	}
	for _, path := range []string{turnPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	// output-config.json through the manager caller in Start.
	e := newTestEnv(t)
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	sdir, err := e.m.Store.Dir(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(sdir, "agents", "1", "output-config.json")
	got, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want = "{\n  \"version\": 1,\n  \"profile\": {\n    \"name\": \"mock\",\n    \"output\": {\n      \"dialect\": \"text\"\n    }\n  }\n}\n"
	if string(got) != want {
		t.Fatalf("output-config bytes = %q; want %q", got, want)
	}
	snapshot := workeroutput.SelectedSnapshot{Version: workeroutput.SchemaVersion, Profile: workeroutput.ProfileSnapshot{Name: "mock", Output: mockProfile("mock").EffectiveOutput()}}
	old, err = json.MarshalIndent(snapshot, "", "  ")
	if err != nil || string(got) != string(append(old, '\n')) {
		t.Fatal("old output-config encoder differs")
	}
	configInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.Mode().Perm() != 0o600 {
		t.Fatalf("output-config mode = %o", configInfo.Mode().Perm())
	}
	e.wait(info.ID, 1)
}

func TestReadMetaIgnoresLegacyDNDUntil(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	const id = "ses_20260102-030405-abcdef01"
	recordtest.WriteRaw(t, s.Root, id, []byte(`{"id":"ses_20260102-030405-abcdef01","name":"old","roomId":"global","room":"global","profile":"mock","dndUntil":"2100-01-02T03:04:05Z"}`))
	meta, err := s.ReadMeta(id)
	if err != nil {
		t.Fatalf("read old session record: %v", err)
	}
	if meta.ID != id || meta.Name != "old" || meta.Profile != "mock" {
		t.Fatalf("old session changed: %+v", meta)
	}
}
