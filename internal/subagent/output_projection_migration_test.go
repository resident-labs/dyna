package subagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

func TestSessionProjectionPreservesOverlappingTurnOffsets(t *testing.T) {
	s, id := turnStore(t)
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"{\"v\":1,\"kind\":\"message\",\"role\":\"user\",\"text\":\"hi\"}\n",
		"{\"v\":1,\"kind\":\"status\",\"name\":\"compact_boundary\"}\n",
		"{\"v\":1,\"kind\":\"message\",\"role\":\"assistant\",\"text\":\"ok\"}\n",
	}
	source := []byte(lines[0] + lines[1] + lines[2])
	path, err := s.NormalizedPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	// Turn 1 ends after the compaction line; turn 2 starts before it.
	first := int64(len(lines[0]) + len(lines[1]))
	for n, turn := range []Turn{
		{N: 1, Status: "ok", NormEnd: first},
		{N: 2, Status: "ok", NormStart: int64(len(lines[0])), NormEnd: int64(len(source))},
	} {
		raw, err := json.Marshal(turn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := saveTurnRow(t.Context(), db, id, n+1, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.migrateOutputProjection(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, source) {
		t.Fatalf("projection changed source bytes: %v", err)
	}
	key := "sessions/" + id + "/agents/1"
	if ok, err := projection.Converted(t.Context(), db, key); err != nil || !ok {
		t.Fatalf("artifact was not checkpointed: %v", err)
	}
	var start, end int64
	var count int
	if err := db.QueryRow("SELECT count(*), min(source_start), min(source_end) FROM output_projection WHERE artifact_key=?", key).Scan(&count, &start, &end); err != nil {
		t.Fatal(err)
	}
	if count != 1 || start != int64(len(lines[0])) || end != first {
		t.Fatalf("mapping = %d rows from %d to %d; want one row for the overlapped line", count, start, end)
	}
}

func TestSessionProjectionUsesSharedOverlapContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lines     []string
		wantState string
		wantID    string
	}{
		{"result", []string{
			"{\"kind\":\"status\",\"name\":\"result\"}\n",
			"{\"kind\":\"message\",\"role\":\"assistant\",\"text\":\"done\"}\n",
		}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id := turnStore(t)
			db, err := s.turnsDB()
			if err != nil {
				t.Fatal(err)
			}
			source := []byte(tc.lines[0] + tc.lines[1])
			path, err := s.NormalizedPath(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			offset := int64(len(tc.lines[0]))
			for n, start := range []int64{0, offset} {
				raw, err := json.Marshal(Turn{N: n + 1, Status: "ok", NormStart: start, NormEnd: int64(len(source)), OutputDialect: workeroutput.DialectClaudeStreamJSON})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := saveTurnRow(t.Context(), db, id, n+1, raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.migrateOutputProjection(); err != nil {
				t.Fatal(err)
			}
			reader, err := projection.Open(filepath.Dir(s.root()), "sessions/"+id+"/agents/1", path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			event, mapped, err := reader.Event(offset, int64(len(source)), []byte(tc.lines[1]))
			if err != nil {
				t.Fatal(err)
			}
			if !mapped {
				if err := json.Unmarshal([]byte(tc.lines[1]), &event); err != nil {
					t.Fatal(err)
				}
			}
			if event.State != tc.wantState || event.ID != tc.wantID {
				t.Fatalf("shared event state=%q id=%q; want state=%q id=%q", event.State, event.ID, tc.wantState, tc.wantID)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, source) {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}
